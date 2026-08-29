package discovery

import (
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/meowrain/localsend-go/internal/utils/logger"

	probing "github.com/prometheus-community/pro-bing"
)

// getLocalIP 获取本地 IP 地址
func GetLocalIP() ([]net.IP, error) {
	ips := make([]net.IP, 0)
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil, err
	}

	for _, iface := range ifaces {
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}

		for _, addr := range addrs {
			switch v := addr.(type) {
			case *net.IPNet:
				if v.IP.To4() != nil && !v.IP.IsLoopback() {
					ips = append(ips, v.IP)
				}
			}
		}
	}
	return ips, nil
}

// pingScan 使用 ICMP ping 扫描局域网内的所有活动设备
func pingScan() ([]string, error) {
	var ips []string
	ipGroup, err := GetLocalIP()
	if err != nil {
		logger.Errorf("[pingScan] GetLocalIP failed: %v", err)
		return nil, err
	}
	logger.Debugf("[pingScan] local IP groups=%v", ipGroup)
	for _, i := range ipGroup {
		ip := i.Mask(net.IPv4Mask(255, 255, 255, 0)) // 假设是 24 子网掩码
		ip4 := ip.To4()
		if ip4 == nil {
			return nil, fmt.Errorf("invalid IPv4 address")
		}

		var wg sync.WaitGroup
		var mu sync.Mutex

		for i := 1; i < 255; i++ {
			ip4[3] = byte(i)
			targetIP := ip4.String()

			wg.Add(1)
			go func(ip string) {
				defer wg.Done()
				pinger, err := probing.NewPinger(ip)
				if err != nil {
					logger.Errorf("Failed to create pinger: %v", err)
					return
				}
				pinger.SetPrivileged(true)
				pinger.Count = 1
				pinger.Timeout = time.Second * 1

				pinger.OnRecv = func(pkt *probing.Packet) {
					mu.Lock()
					ips = append(ips, ip)
					mu.Unlock()
				}
				err = pinger.Run()
				if err != nil {
					// 忽视发送ping失败
					return
				}
			}(targetIP)
		}

		wg.Wait()
		logger.Debugf("[pingScan] subnet scan done ip=%v found %d active hosts", ip, len(ips))
	}
	logger.Debugf("[pingScan] total discovered %d hosts: %v", len(ips), ips)
	return ips, nil
}

// IsLocalIP 判断是否为本机 IP
func IsLocalIP(ipStr string) bool {
	ips, err := GetLocalIP()
	if err != nil {
		return false
	}
	for _, ip := range ips {
		if ip.String() == ipStr {
			return true
		}
	}
	return false
}
