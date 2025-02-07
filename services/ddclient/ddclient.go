package ddclient

// Based on github.com/clayshek/google-ddns-updater

import (
	"context"
	"fmt"
	"io"
	"io/ioutil"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	config_proto "www.velocidex.com/golang/velociraptor/config/proto"
	"www.velocidex.com/golang/velociraptor/logging"
	"www.velocidex.com/golang/velociraptor/vql/networking"
)

var (
	ddns_service = "domains.google.com"
)

type DynDNSService struct {
	config_obj *config_proto.Config

	external_ip_url string
	dns_server      string
}

func (self *DynDNSService) updateIP(config_obj *config_proto.Config) {
	if config_obj.Frontend == nil || config_obj.Frontend.DynDns == nil {
		return
	}

	logger := logging.GetLogger(config_obj, &logging.FrontendComponent)
	logger.Info("Checking DNS with %v", self.external_ip_url)

	externalIP, err := self.GetExternalIp()
	if err != nil {
		logger.Error("Unable to get external IP: %v", err)
		return
	}

	ddns_hostname := config_obj.Frontend.Hostname
	hostnameIPs, err := self.GetCurrentDDNSIp(ddns_hostname)
	if err != nil {
		logger.Error("Unable to resolve DDNS hostname IP: %v", err)
		return
	}

	for _, ip := range hostnameIPs {
		if ip == externalIP {
			return
		}
	}

	logger.Info("DNS UPDATE REQUIRED. External IP=%v. %v=%v.",
		externalIP, ddns_hostname, hostnameIPs)

	reqstr := fmt.Sprintf(
		"https://%v/nic/update?hostname=%v&myip=%v",
		ddns_service,
		ddns_hostname,
		externalIP)
	logger.Debug("Submitting update request to %v", reqstr)

	err = UpdateDDNSRecord(
		config_obj,
		reqstr,
		config_obj.Frontend.DynDns.DdnsUsername,
		config_obj.Frontend.DynDns.DdnsPassword)
	if err != nil {
		logger.Error("Failed to update: %v", err)
		return
	}
}

func (self *DynDNSService) Start(
	ctx context.Context, config_obj *config_proto.Config) {

	if config_obj.Frontend == nil || config_obj.Frontend.DynDns == nil {
		return
	}

	logger := logging.GetLogger(config_obj, &logging.FrontendComponent)
	logger.Info("<green>Starting</> the DynDNS service: Updating hostname %v with checkip URL %v",
		config_obj.Frontend.Hostname, self.external_ip_url)

	min_update_wait := config_obj.Frontend.DynDns.Frequency
	if min_update_wait == 0 {
		min_update_wait = 60
	}

	// First time check immediately.
	self.updateIP(config_obj)

	for {
		select {
		case <-ctx.Done():
			return

			// Do not try to update sooner than this or we
			// get banned. It takes a while for dns
			// records to propagate.
		case <-time.After(time.Duration(min_update_wait) * time.Second):
			self.updateIP(config_obj)
		}
	}
}

func StartDynDNSService(
	ctx context.Context,
	wg *sync.WaitGroup,
	config_obj *config_proto.Config) error {

	if config_obj.Frontend == nil ||
		config_obj.Frontend.DynDns == nil ||
		config_obj.Frontend.DynDns.DdnsUsername == "" ||
		config_obj.Frontend.Hostname == "" {
		return nil
	}

	result := &DynDNSService{
		config_obj:      config_obj,
		external_ip_url: config_obj.Frontend.DynDns.CheckipUrl,
		dns_server:      config_obj.Frontend.DynDns.DnsServer,
	}

	// Set sensible defaults that should work reliably most of the
	// time.
	if result.external_ip_url == "" {
		result.external_ip_url = "https://domains.google.com/checkip"
	}

	if result.dns_server == "" {
		result.dns_server = "8.8.8.8:53"
	}

	wg.Add(1)
	go func() {
		defer wg.Done()

		result.Start(ctx, config_obj)
	}()

	return nil
}

func (self *DynDNSService) GetExternalIp() (string, error) {
	resp, err := http.Get(self.external_ip_url)
	if err != nil {
		return "Unable to determine external IP: %v ", err
	}
	defer resp.Body.Close()
	ip, err := ioutil.ReadAll(resp.Body)
	result := strings.TrimSpace(string(ip))

	if err != nil && err != io.EOF {
		return result, err
	}

	return result, nil
}

func (self *DynDNSService) GoogleDNSDialer(ctx context.Context, network, address string) (net.Conn, error) {
	d := net.Dialer{}
	return d.DialContext(ctx, "udp", self.dns_server)
}

func (self *DynDNSService) GetCurrentDDNSIp(fqdn string) ([]string, error) {
	r := net.Resolver{
		PreferGo: true,
		Dial:     self.GoogleDNSDialer,
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ips, err := r.LookupHost(ctx, fqdn)
	if err != nil {
		return nil, err
	}
	return ips, nil
}

func UpdateDDNSRecord(config_obj *config_proto.Config,
	url, user, pw string) error {
	logger := logging.GetLogger(config_obj, &logging.FrontendComponent)

	client := &http.Client{
		CheckRedirect: nil,
		Transport: &http.Transport{
			Proxy: networking.GetProxy(),
		},
	}

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "")
	req.SetBasicAuth(user, pw)

	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return err
	}

	logger.Debug("Update response: %v", string(body))

	return nil
}
