package models

// SendModel 用于 TUI 选择
type SendModel struct {
	DeviceName string
	IP         string
	Port       int
	Protocol   string // http 或 https
}
