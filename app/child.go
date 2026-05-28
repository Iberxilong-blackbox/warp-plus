package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/netip"
	"path/filepath"
	"time"

	"github.com/bepass-org/warp-plus/egresscheck"
	"github.com/bepass-org/warp-plus/psiphon"
	"github.com/bepass-org/warp-plus/warp"
	"github.com/bepass-org/warp-plus/wireguard/tun/netstack"
	"github.com/bepass-org/warp-plus/wiresocks"
)

// ChildConfig carries the CLI flags specific to child mode.
type ChildConfig struct {
	Enabled    bool
	ID         int
	Token      string
	ParentAddr string
}

type childRegistration struct {
	ChildID int    `json:"child_id"`
	Token   string `json:"token"`
	Port    int    `json:"port"`
	IP      string `json:"ip"`
}

func RunChild(ctx context.Context, l *slog.Logger, opts WarpOptions, cfg ChildConfig) error {
	l = l.With("child_id", cfg.ID)
	cacheDir := opts.CacheDir
	if cacheDir == "" {
		cacheDir = filepath.Join("warp_plus_cache", fmt.Sprintf("child_%d", cfg.ID))
	}

	// ---- WARP setup ----
	endpoints := []string{opts.Endpoint, opts.Endpoint}
	if opts.Scan != nil {
		ident, err := warp.LoadOrCreateIdentity(l, filepath.Join(cacheDir, "primary"), opts.License)
		if err != nil {
			return fmt.Errorf("child: load warp identity: %w", err)
		}
		opts.Scan.PrivateKey = ident.PrivateKey
		opts.Scan.PublicKey = ident.Config.Peers[0].PublicKey

		res, err := wiresocks.RunScan(ctx, l, *opts.Scan)
		if err != nil {
			return fmt.Errorf("child: scan: %w", err)
		}
		endpoints = make([]string, len(res))
		for i := range res {
			endpoints[i] = res[i].AddrPort.String()
		}
	}
	l.Info("child using warp endpoints", "endpoints", endpoints)

	ident, err := warp.LoadOrCreateIdentity(l, filepath.Join(cacheDir, "primary"), opts.License)
	if err != nil {
		return fmt.Errorf("child: load warp identity: %w", err)
	}
	conf := generateWireguardConfig(ident)
	conf.Interface.MTU = singleMTU
	conf.Interface.DNS = []netip.Addr{opts.DnsAddr}

	for i, peer := range conf.Peers {
		peer.Endpoint = endpoints[0]
		peer.Trick = true
		peer.KeepAlive = 5
		if opts.Reserved != "" {
			r, err := wiresocks.ParseReserved(opts.Reserved)
			if err != nil {
				return fmt.Errorf("child: parse reserved: %w", err)
			}
			peer.Reserved = r
		}
		conf.Peers[i] = peer
	}

	var tnet *netstack.Net
	for _, t := range []string{"t1", "t2"} {
		tunDev, tn, werr := netstack.CreateNetTUN(conf.Interface.Addresses, conf.Interface.DNS, conf.Interface.MTU)
		if werr != nil {
			continue
		}
		if werr = establishWireguard(l, &conf, tunDev, opts.FwMark, t); werr != nil {
			continue
		}
		if werr = usermodeTunTest(ctx, l, tn, opts.TestURL); werr != nil {
			continue
		}
		tnet = tn
		break
	}
	if tnet == nil {
		return fmt.Errorf("child: wireguard setup failed for all attempts")
	}

	warpBind, err := wiresocks.StartProxy(ctx, l, tnet, netip.MustParseAddrPort("127.0.0.1:0"))
	if err != nil {
		return fmt.Errorf("child: start warp proxy: %w", err)
	}

	// ---- Psiphon + egress check ----
	checker, err := egresscheck.New(*opts.EgressCheck)
	if err != nil {
		return fmt.Errorf("child: create egress checker: %w", err)
	}

	maxRetry := opts.EgressCheck.MaxRetry
	if maxRetry <= 0 {
		maxRetry = 3
	}

	var tunnel *psiphon.Tunnel
	var psiphonAddr netip.AddrPort
	var checkResult egresscheck.Result

	for attempt := 1; attempt <= maxRetry; attempt++ {
		addr, err := freeLocalAddr()
		if err != nil {
			return fmt.Errorf("child: free local addr: %w", err)
		}

		l.Info("child starting psiphon handshake", "attempt", attempt, "max_retry", maxRetry)
		tunnel, err = psiphon.StartPsiphon(ctx, l, warpBind, cacheDir, addr, opts.Psiphon.Country)
		if err != nil {
			l.Warn("child psiphon handshake failed", "attempt", attempt, "error", err)
			continue
		}

		checkResult, err = checker.Check(ctx, addr)
		if err != nil {
			l.Warn("child egress check failed", "attempt", attempt, "error", err)
			tunnel.Close()
			continue
		}

		if !checkResult.Pass {
			l.Info("child egress rejected", "attempt", attempt, "ip", checkResult.IP, "reason", checkResult.Reason)
			tunnel.Close()
			continue
		}

		psiphonAddr = addr
		l.Info("child egress accepted", "attempt", attempt, "ip", checkResult.IP, "port", addr.Port())
		break
	}

	if tunnel == nil {
		return fmt.Errorf("child: unable to establish acceptable egress after %d attempts", maxRetry)
	}
	defer tunnel.Close()

	// ---- Register with parent ----
	if err := registerWithParent(cfg.ParentAddr, cfg.ID, cfg.Token, int(psiphonAddr.Port()), checkResult.IP.String()); err != nil {
		return fmt.Errorf("child: register with parent: %w", err)
	}

	l.Info("child ready and registered", "port", psiphonAddr.Port(), "ip", checkResult.IP)

	// Block until parent kills us.
	<-ctx.Done()
	l.Info("child exiting")
	return nil
}

func registerWithParent(parentAddr string, childID int, token string, port int, ip string) error {
	body := childRegistration{
		ChildID: childID,
		Token:   token,
		Port:    port,
		IP:      ip,
	}

	data, err := json.Marshal(body)
	if err != nil {
		return err
	}

	url := fmt.Sprintf("http://%s/child/register", parentAddr)
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	client := http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		var result map[string]string
		_ = json.NewDecoder(resp.Body).Decode(&result)
		return fmt.Errorf("parent rejected registration: %s (status=%d)", result["status"], resp.StatusCode)
	}

	return nil
}

// FreeLocalAddrTCP returns a random free TCP port on loopback.
// Exported for use in tests.
func FreeLocalAddrTCP() (netip.AddrPort, error) {
	return freeLocalAddr()
}
