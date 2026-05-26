package egresscheck

import (
	"bufio"
	"fmt"
	"net/netip"
	"os"
	"strings"
)

type Blacklist struct {
	rules []blacklistRule
}

type blacklistRule struct {
	raw    string
	ip     netip.Addr
	prefix netip.Prefix
	text   string
	kind   ruleKind
}

type ruleKind int

const (
	ruleIP ruleKind = iota
	ruleCIDR
	rulePrefix
)

func LoadBlacklist(path string) (*Blacklist, error) {
	if path == "" {
		return nil, nil
	}

	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	var rules []blacklistRule
	scanner := bufio.NewScanner(file)
	lineNo := 0
	for scanner.Scan() {
		lineNo++
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		rule, err := parseRule(line)
		if err != nil {
			return nil, fmt.Errorf("invalid blacklist rule %s:%d: %w", path, lineNo, err)
		}
		rules = append(rules, rule)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}

	return &Blacklist{rules: rules}, nil
}

func parseRule(line string) (blacklistRule, error) {
	if strings.Contains(line, "*") {
		if !strings.HasSuffix(line, "*") || strings.Count(line, "*") != 1 {
			return blacklistRule{}, fmt.Errorf("only a trailing * prefix rule is supported")
		}
		prefix := strings.TrimSuffix(line, "*")
		if prefix == "" {
			return blacklistRule{}, fmt.Errorf("empty prefix")
		}
		return blacklistRule{raw: line, text: prefix, kind: rulePrefix}, nil
	}

	if strings.Contains(line, "/") {
		prefix, err := netip.ParsePrefix(line)
		if err != nil {
			return blacklistRule{}, err
		}
		return blacklistRule{raw: line, prefix: prefix, kind: ruleCIDR}, nil
	}

	ip, err := netip.ParseAddr(line)
	if err != nil {
		return blacklistRule{}, err
	}
	return blacklistRule{raw: line, ip: ip, kind: ruleIP}, nil
}

func (b *Blacklist) Match(ip netip.Addr) (string, bool) {
	if b == nil {
		return "", false
	}
	ipText := ip.String()
	for _, rule := range b.rules {
		switch rule.kind {
		case ruleIP:
			if rule.ip == ip {
				return rule.raw, true
			}
		case ruleCIDR:
			if rule.prefix.Contains(ip) {
				return rule.raw, true
			}
		case rulePrefix:
			if strings.HasPrefix(ipText, rule.text) {
				return rule.raw, true
			}
		}
	}
	return "", false
}
