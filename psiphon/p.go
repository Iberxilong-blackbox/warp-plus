package psiphon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/netip"
	"path/filepath"
	"sync"
	"time"

	"github.com/Psiphon-Labs/psiphon-tunnel-core/psiphon"
)

var Countries = []string{
	"AT",
	"AU",
	"BE",
	"BG",
	"CA",
	"CH",
	"CZ",
	"DE",
	"DK",
	"EE",
	"ES",
	"FI",
	"FR",
	"GB",
	"HR",
	"HU",
	"IE",
	"IN",
	"IT",
	"JP",
	"LV",
	"NL",
	"NO",
	"PL",
	"PT",
	"RO",
	"RS",
	"SE",
	"SG",
	"SK",
	"US",
}

// NoticeEvent represents the notices emitted by tunnel core. It will be passed to
// noticeReceiver, if supplied.
// NOTE: Ordinary users of this library should never need this.
type NoticeEvent struct {
	Data      map[string]interface{} `json:"data"`
	Type      string                 `json:"noticeType"`
	Timestamp string                 `json:"timestamp"`
}

type ServerInfo struct {
	IP           string
	Region       string
	ProviderID   string
	DiagnosticID string
	Protocol     string
}

type Tunnel struct {
	cancel context.CancelFunc
	done   chan struct{}
	Server ServerInfo
}

func (t *Tunnel) Close() {
	t.cancel()
	select {
	case <-t.done:
	case <-time.After(5 * time.Second):
	}
	psiphon.CloseDataStore()
	psiphon.SetNoticeWriter(io.Discard)
}

type observedServerInfo struct {
	mu   sync.Mutex
	info ServerInfo
}

func (o *observedServerInfo) updateFromNotice(event NoticeEvent) {
	if event.Type != "ConnectedServer" && event.Type != "ActiveTunnel" {
		return
	}

	o.mu.Lock()
	defer o.mu.Unlock()

	if v := noticeString(event.Data, "diagnosticID"); v != "" {
		o.info.DiagnosticID = v
	}
	if v := noticeString(event.Data, "region"); v != "" {
		o.info.Region = v
	}
	if v := noticeString(event.Data, "serverRegion"); v != "" {
		o.info.Region = v
	}
	if v := noticeString(event.Data, "providerID"); v != "" {
		o.info.ProviderID = v
	}
	if v := noticeString(event.Data, "protocol"); v != "" {
		o.info.Protocol = v
	}
}

func (o *observedServerInfo) snapshot() ServerInfo {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.info
}

func noticeString(data map[string]interface{}, key string) string {
	value, ok := data[key]
	if !ok || value == nil {
		return ""
	}
	s, ok := value.(string)
	if !ok {
		return ""
	}
	return s
}

func lookupConnectedServerInfo(config *psiphon.Config, observed ServerInfo) (ServerInfo, error) {
	if observed.DiagnosticID == "" {
		return observed, nil
	}

	_, iterator, err := psiphon.NewServerEntryIterator(config)
	if err != nil {
		return observed, err
	}
	defer iterator.Close()

	for {
		serverEntry, err := iterator.Next()
		if err != nil {
			return observed, err
		}
		if serverEntry == nil {
			return observed, nil
		}
		if serverEntry.GetDiagnosticID() != observed.DiagnosticID {
			continue
		}

		info := observed
		info.IP = serverEntry.IpAddress
		if info.Region == "" {
			info.Region = serverEntry.Region
		}
		if info.ProviderID == "" {
			info.ProviderID = serverEntry.ProviderID
		}
		return info, nil
	}
}

func StartTunnel(ctx context.Context, l *slog.Logger, config *psiphon.Config) (*Tunnel, error) {
	controllerCtx, cancel := context.WithCancel(ctx)
	// config.Commit must be called before calling config.SetParameters
	// or attempting to connect.
	if err := config.Commit(true); err != nil {
		cancel()
		return nil, errors.New("config.Commit failed")
	}

	// Will receive a value when the tunnel has successfully connected.
	connected := make(chan struct{})
	// Will receive a value if an error occurs during the connection sequence.
	errored := make(chan error)
	observedServer := &observedServerInfo{}

	// Set up notice handling
	psiphon.SetNoticeWriter(psiphon.NewNoticeReceiver(
		func(notice []byte) {
			var event NoticeEvent
			if err := json.Unmarshal(notice, &event); err != nil {
				return
			}
			observedServer.updateFromNotice(event)

			go func(event NoticeEvent) {
				l.Debug("psiphon core notice", "type", event.Type, "data", event.Data)
				switch event.Type {
				case "EstablishTunnelTimeout":
					select {
					case errored <- errors.New("clientlib: tunnel establishment timeout"):
					default:
					}
				case "Tunnels":
					if event.Data["count"].(float64) > 0 {
						select {
						case connected <- struct{}{}:
						default:
						}
					}
				}
			}(event)
		}))

	if err := psiphon.OpenDataStore(config); err != nil {
		cancel()
		return nil, fmt.Errorf("failed to open data store: %w", err)
	}

	if err := psiphon.ImportEmbeddedServerEntries(controllerCtx, config, "", ""); err != nil {
		cancel()
		psiphon.CloseDataStore()
		psiphon.SetNoticeWriter(io.Discard)
		return nil, err
	}

	// Create the Psiphon controller
	controller, err := psiphon.NewController(config)
	if err != nil {
		cancel()
		psiphon.CloseDataStore()
		psiphon.SetNoticeWriter(io.Discard)
		return nil, errors.New("psiphon.NewController failed")
	}

	done := make(chan struct{})
	// Begin tunnel connection
	go func() {
		defer close(done)
		// Start the tunnel. Only returns on error (or internal timeout).
		controller.Run(controllerCtx)

		select {
		case errored <- errors.New("controller.Run exited unexpectedly"):
		default:
		}
	}()

	// Wait for an active tunnel or error
	establishTimeout := 75 * time.Second
	if config.EstablishTunnelTimeoutSeconds != nil && *config.EstablishTunnelTimeoutSeconds > 0 {
		establishTimeout = time.Duration(*config.EstablishTunnelTimeoutSeconds+15) * time.Second
	}
	timer := time.NewTimer(establishTimeout)
	defer timer.Stop()

	select {
	case <-connected:
		serverInfo := observedServer.snapshot()
		lookedUpServerInfo, err := lookupConnectedServerInfo(config, serverInfo)
		if err != nil {
			l.Warn("unable to resolve psiphon connected server entry", "diagnostic_id", serverInfo.DiagnosticID, "error", err)
		} else {
			serverInfo = lookedUpServerInfo
		}
		if serverInfo.DiagnosticID != "" || serverInfo.IP != "" {
			l.Info(
				"psiphon selected server",
				"server_entry_ip", serverInfo.IP,
				"server_entry_region", serverInfo.Region,
				"server_entry_provider_id", serverInfo.ProviderID,
				"server_entry_diagnostic_id", serverInfo.DiagnosticID,
				"protocol", serverInfo.Protocol,
			)
		}
		return &Tunnel{cancel: cancel, done: done, Server: serverInfo}, nil
	case err := <-errored:
		cancel()
		psiphon.CloseDataStore()
		psiphon.SetNoticeWriter(io.Discard)
		return nil, err
	case <-timer.C:
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
		}
		psiphon.CloseDataStore()
		psiphon.SetNoticeWriter(io.Discard)
		return nil, fmt.Errorf("clientlib: tunnel establishment hard timeout after %s", establishTimeout)
	case <-controllerCtx.Done():
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
		}
		psiphon.CloseDataStore()
		psiphon.SetNoticeWriter(io.Discard)
		return nil, controllerCtx.Err()
	}
}

func StartPsiphon(ctx context.Context, l *slog.Logger, wgBind netip.AddrPort, dir string, localSocksAddr netip.AddrPort, country string) (*Tunnel, error) {
	host := ""
	if !netip.MustParsePrefix("127.0.0.0/8").Contains(localSocksAddr.Addr()) {
		host = "any"
	}

	timeout := 60
	config := psiphon.Config{
		EgressRegion:                                 country,
		ListenInterface:                              host,
		LocalSocksProxyPort:                          int(localSocksAddr.Port()),
		UpstreamProxyURL:                             fmt.Sprintf("socks5://%s", wgBind),
		DisableLocalHTTPProxy:                        true,
		PropagationChannelId:                         "FFFFFFFFFFFFFFFF",
		RemoteServerListDownloadFilename:             "remote_server_list",
		RemoteServerListSignaturePublicKey:           "MIICIDANBgkqhkiG9w0BAQEFAAOCAg0AMIICCAKCAgEAt7Ls+/39r+T6zNW7GiVpJfzq/xvL9SBH5rIFnk0RXYEYavax3WS6HOD35eTAqn8AniOwiH+DOkvgSKF2caqk/y1dfq47Pdymtwzp9ikpB1C5OfAysXzBiwVJlCdajBKvBZDerV1cMvRzCKvKwRmvDmHgphQQ7WfXIGbRbmmk6opMBh3roE42KcotLFtqp0RRwLtcBRNtCdsrVsjiI1Lqz/lH+T61sGjSjQ3CHMuZYSQJZo/KrvzgQXpkaCTdbObxHqb6/+i1qaVOfEsvjoiyzTxJADvSytVtcTjijhPEV6XskJVHE1Zgl+7rATr/pDQkw6DPCNBS1+Y6fy7GstZALQXwEDN/qhQI9kWkHijT8ns+i1vGg00Mk/6J75arLhqcodWsdeG/M/moWgqQAnlZAGVtJI1OgeF5fsPpXu4kctOfuZlGjVZXQNW34aOzm8r8S0eVZitPlbhcPiR4gT/aSMz/wd8lZlzZYsje/Jr8u/YtlwjjreZrGRmG8KMOzukV3lLmMppXFMvl4bxv6YFEmIuTsOhbLTwFgh7KYNjodLj/LsqRVfwz31PgWQFTEPICV7GCvgVlPRxnofqKSjgTWI4mxDhBpVcATvaoBl1L/6WLbFvBsoAUBItWwctO2xalKxF5szhGm8lccoc5MZr8kfE0uxMgsxz4er68iCID+rsCAQM=",
		RemoteServerListUrl:                          "https://s3.amazonaws.com//psiphon/web/mjr4-p23r-puwl/server_list_compressed",
		SponsorId:                                    "FFFFFFFFFFFFFFFF",
		NetworkID:                                    "test",
		ClientPlatform:                               "Android_4.0.4_com.example.exampleClientLibraryApp",
		AllowDefaultDNSResolverWithBindToDevice:      true,
		EstablishTunnelTimeoutSeconds:                &timeout,
		DataRootDirectory:                            dir,
		MigrateDataStoreDirectory:                    dir,
		MigrateObfuscatedServerListDownloadDirectory: dir,
		MigrateRemoteServerListDownloadFilename:      filepath.Join(dir, "server_list_compressed"),
	}

	l.Info(
		"starting handshake",
		"data_root", config.DataRootDirectory,
		"migrate_data_store", config.MigrateDataStoreDirectory,
		"migrate_server_list", config.MigrateRemoteServerListDownloadFilename,
	)
	tunnel, err := StartTunnel(ctx, l, &config)
	if err != nil {
		return nil, fmt.Errorf("Unable to start psiphon: %w", err)
	}
	l.Info("psiphon started successfully")
	return tunnel, nil
}

func RunPsiphon(ctx context.Context, l *slog.Logger, wgBind netip.AddrPort, dir string, localSocksAddr netip.AddrPort, country string) error {
	tunnel, err := StartPsiphon(ctx, l, wgBind, dir, localSocksAddr, country)
	if err != nil {
		return err
	}
	go func() {
		<-ctx.Done()
		tunnel.Close()
	}()
	return nil
}
