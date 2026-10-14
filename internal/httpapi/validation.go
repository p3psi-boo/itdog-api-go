package httpapi

import (
	"errors"
	"net"
	"net/url"
	"strconv"
	"strings"

	"itdog-api-golang/internal/itdog"
)

// Validate syntax only: the upstream measurement nodes own DNS resolution.
func validHost(host string) bool {
	if ip := net.ParseIP(host); ip != nil {
		return ip.To4() != nil
	}
	return validName(host, false)
}

func validDNSName(name string) bool { return validName(name, true) }

func validName(name string, underscore bool) bool {
	name = strings.TrimSuffix(name, ".")
	if len(name) == 0 || len(name) > 253 {
		return false
	}
	for _, label := range strings.Split(name, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, r := range label {
			if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || underscore && r == '_' {
				continue
			}
			return false
		}
	}
	return true
}

func validURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" {
		return false
	}
	if !validHost(u.Hostname()) && net.ParseIP(u.Hostname()) == nil {
		return false
	}
	if strings.HasSuffix(u.Host, ":") {
		return false
	}
	if port := u.Port(); port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return false
		}
	}
	return true
}

func validDNSServer(server string) bool { return net.ParseIP(server) != nil }

func validateHTTPOptions(opt itdog.HTTPOptions) error {
	if opt.RedirectNum != nil && (*opt.RedirectNum < 0 || *opt.RedirectNum > 10) {
		return errors.New("redirect_num 必须在 0 到 10 之间")
	}
	if !oneOf(opt.HTTPVersion, "", "auto", "http_1_1", "http_2", "http_3") {
		return errors.New("无效的 http_version")
	}
	if !oneOf(opt.CheckMode, "", "fast", "slow") {
		return errors.New("无效的 check_mode")
	}
	if !oneOf(opt.DNSServerType, "", "isp", "custom") {
		return errors.New("无效的 dns_server_type")
	}
	if opt.DNSServerType == "custom" && !validDNSServer(opt.DNSServer) {
		return errors.New("自定义 DNS 需要有效的 dns_server IP")
	}
	if opt.DNSServer != "" && !validDNSServer(opt.DNSServer) {
		return errors.New("无效的 dns_server")
	}
	if opt.IPv4 != "" {
		if ip := net.ParseIP(opt.IPv4); ip == nil || ip.To4() == nil {
			return errors.New("无效的 ipv4")
		}
	}
	return nil
}

func oneOf(value string, choices ...string) bool {
	for _, choice := range choices {
		if value == choice {
			return true
		}
	}
	return false
}
