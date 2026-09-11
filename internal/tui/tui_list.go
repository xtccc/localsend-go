package tui

import (
	"fmt"
	"os"
	"time"

	"github.com/xtccc/localsend-go/internal/models"
	"github.com/xtccc/localsend-go/internal/utils/logger"

	bubbletea "github.com/charmbracelet/bubbletea"
)

// selectDevice 使用 Bubble Tea 库显示可供选择的设备列表并等待用户选择
// 返回选中的设备完整信息（包含 Protocol/Port），调用方应据此构造 URL
func SelectDevice(updates <-chan []models.SendModel) (models.SendModel, error) {
	// 方案B：--debug 时将日志落盘，避免 TUI 与 logger 抢终端导致折行
	var logFile *os.File
	var origOut = logger.GetOutput()
	if logger.IsDebugEnabled() {
		f, err := os.OpenFile("debug.log", os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
		if err == nil {
			logFile = f
			logger.SetOutput(f)
			// 同时让 bubbletea 的调试日志也落盘（若有）
			// 注意：defer 在此函数结束时恢复
			defer func() {
				_ = f.Sync()
				logger.SetOutput(origOut)
				_ = f.Close()
				logger.Infof("[TUI] debug logs during device selection written to debug.log")
			}()
			logger.Debugf("[TUI] debug logging redirected to debug.log during device selection")
		} else {
			logger.Warnf("[TUI] failed to open debug.log: %v", err)
		}
	}
	_ = logFile // 避免未使用警告（仅 debug 时使用）

	// 创建一个带缓冲的内部 channel
	internalUpdates := make(chan []models.SendModel, 100)

	// 在后台持续从外部 channel 读取更新
	go func() {
		for devices := range updates {
			// 非阻塞方式发送到内部 channel
			select {
			case internalUpdates <- devices:
			default:
				// 如果 channel 满了，清空后重新发送
				select {
				case <-internalUpdates:
				default:
				}
				internalUpdates <- devices
			}
		}
	}()

	// 创建模型和 Bubble Tea 程序
	initModel := &model{
		devices:    []models.SendModel{},
		deviceMap:  make(map[string]models.SendModel),
		sortedKeys: make([]string, 0),
		cursor:     0,
		updates:    internalUpdates,
	}

	cmd := bubbletea.NewProgram(initModel)
	m, err := cmd.Run()
	if err != nil {
		return models.SendModel{}, err
	}

	if m, ok := m.(model); ok && len(m.devices) > 0 {
		return m.devices[m.cursor], nil
	}
	return models.SendModel{}, nil
}

// model 结构体用于 Bubble Tea
type model struct {
	devices    []models.SendModel
	deviceMap  map[string]models.SendModel // 使用 IP 作为键来存储设备
	sortedKeys []string                    // 保持固定的显示顺序
	cursor     int
	updates    <-chan []models.SendModel
}

// TickMsg 用于定期触发更新
type TickMsg time.Time

// Init 实现 Bubble Tea 的 Init 方法
func (m model) Init() bubbletea.Cmd {
	return tick()
}

// tick 每秒钟触发一次
func tick() bubbletea.Cmd {
	return bubbletea.Tick(time.Second, func(t time.Time) bubbletea.Msg {
		return TickMsg(t)
	})
}

// Update 实现 Bubble Tea 的 Update 方法
func (m model) Update(msg bubbletea.Msg) (bubbletea.Model, bubbletea.Cmd) {
	switch msg := msg.(type) {
	case bubbletea.KeyMsg:
		switch msg.String() {
		case "q", "ctrl+c":
			return m, bubbletea.Quit
		case "down", "j":
			if len(m.devices) > 0 {
				m.cursor = (m.cursor + 1) % len(m.devices) // 向下移动
			}
		case "up", "k":
			if len(m.devices) > 0 {
				m.cursor = (m.cursor - 1 + len(m.devices)) % len(m.devices) // 向上移动
			}
		case "enter":
			return m, bubbletea.Quit // 退出选择
		}
	case TickMsg:
		select {
		case newDevices := <-m.updates:
			if m.deviceMap == nil {
				m.deviceMap = make(map[string]models.SendModel)
			}

			// 更新设备映射
			changed := false
			for _, device := range newDevices {
				if _, exists := m.deviceMap[device.IP]; !exists {
					m.deviceMap[device.IP] = device
					m.sortedKeys = append(m.sortedKeys, device.IP)
					changed = true
				}
			}

			// 只有在有新设备时才更新设备列表
			if changed {
				m.devices = make([]models.SendModel, 0, len(m.deviceMap))
				for _, key := range m.sortedKeys {
					if device, ok := m.deviceMap[key]; ok {
						m.devices = append(m.devices, device)
					}
				}

				// 确保光标不会超出设备列表范围
				if m.cursor >= len(m.devices) {
					m.cursor = len(m.devices) - 1
				}
			}
		default:
		}
		return m, tick()
	}
	return m, nil
}

// View 实现 Bubble Tea 的 View 方法
func (m model) View() string {
	if len(m.devices) == 0 {
		return "Scanning Devices...\n\n Press Ctrl+C to exit"
	}

	s := "Found Devices:\n\n"
	for i, device := range m.devices {
		cursor := " " // 默认没有光标
		if m.cursor == i {
			cursor = ">" // 选中的光标
		}
		s += fmt.Sprintf("%s %s (%s)\n", cursor, device.DeviceName, device.IP)
	}
	s += "\nUse arrow keys to navigate and enter to select. Press Ctrl+C to exit."
	return s
}
