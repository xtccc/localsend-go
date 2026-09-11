package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	bubbletea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/meowrain/localsend-go/internal/config"
	"github.com/meowrain/localsend-go/internal/discovery"
	"github.com/meowrain/localsend-go/internal/handlers"
	"github.com/meowrain/localsend-go/internal/pkg/server"
	"github.com/meowrain/localsend-go/internal/utils/logger"
	"github.com/meowrain/localsend-go/static"
	"github.com/sirupsen/logrus"
	qrcode "github.com/skip2/go-qrcode"
)

type textInputModel struct {
	value       string
	cursor      int
	placeholder string
	done        bool
}

func initialTextInputModel() textInputModel {
	return textInputModel{
		value:       "",
		cursor:      0,
		placeholder: "Enter file path...",
		done:        false,
	}
}

func (m textInputModel) Init() bubbletea.Cmd {
	return nil
}

func getPathSuggestions(input string) []string {
	if input == "" {
		input = "."
	}

	dir := input
	if !strings.HasSuffix(input, string(os.PathSeparator)) {
		dir = filepath.Dir(input)
	}

	files, err := filepath.Glob(filepath.Join(dir, "*"))
	if err != nil {
		return nil
	}

	prefix := filepath.Clean(input)
	var suggestions []string
	for _, file := range files {
		if strings.HasPrefix(filepath.Clean(file), prefix) {
			suggestions = append(suggestions, file)
		}
	}
	return suggestions
}

func (m textInputModel) Update(msg bubbletea.Msg) (textInputModel, bubbletea.Cmd) {
	switch msg := msg.(type) {
	case bubbletea.MouseMsg:
		// 忽略鼠标事件
		return m, nil

	case bubbletea.KeyMsg:
		switch msg.String() {
		case "backspace":
			if m.cursor > 0 {
				m.value = m.value[:m.cursor-1] + m.value[m.cursor:]
				m.cursor--
			}
		case "left":
			if m.cursor > 0 {
				m.cursor--
			}
		case "right":
			if m.cursor < len(m.value) {
				m.cursor++
			}
		case "tab":
			suggestions := getPathSuggestions(m.value)
			if len(suggestions) > 0 {
				m.value = suggestions[0]
				m.cursor = len(m.value)
			}
		case "home":
			m.cursor = 0
		case "end":
			m.cursor = len(m.value)
		case "up", "down":
			// Ignore up and down key+s

		case "enter":
			m.done = true

		default:
			if msg.String() != "enter" && msg.String() != "home" && msg.String() != "end" {
				// 只允许输入有效的路径字符
				char := msg.String()
				// 检查是否是有效的路径字符
				if char == "." || char == "/" || char == "\\" || char == ":" || char == "-" || char == "_" ||
					(char >= "a" && char <= "z") || (char >= "A" && char <= "Z") || (char >= "0" && char <= "9") {
					m.value = m.value[:m.cursor] + char + m.value[m.cursor:]
					m.cursor++
				}
			}
		}
	}
	return m, nil
}

func (m textInputModel) View() string {
	if len(m.value) == 0 {
		return m.placeholder
	}
	value := m.value
	cursor := m.cursor
	if cursor > len(value) {
		cursor = len(value)
	}
	return value[:cursor] + "_" + value[cursor:]
}

func (m textInputModel) Value() string {
	return m.value
}

type model struct {
	mode        string
	choices     []string
	cursor      int
	filePrompt  bool
	textInput   textInputModel
	suggestions []string
}

func initialModel() model {
	return model{
		mode:      "",
		choices:   []string{"📤 Send", "📥 Receive", "🌎 Web", "❌ Exit"},
		cursor:    0,
		textInput: initialTextInputModel(),
	}
}

func (m model) Init() bubbletea.Cmd {
	return m.textInput.Init()
}

var (
	titleStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("#7571F9")).
			Border(lipgloss.RoundedBorder()).
			Padding(0, 2).
			MarginBottom(1)

	menuStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#FAFAFA")).
			PaddingLeft(4)

	selectedItemStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("#7571F9")).
				PaddingLeft(2).
				SetString("❯ ")

	unselectedItemStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("#FAFAFA")).
				PaddingLeft(4)

	inputPromptStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("#7571F9")).
				PaddingLeft(2)

	inputStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#FAFAFA")).
			PaddingLeft(1)
)

func (m model) Update(msg bubbletea.Msg) (bubbletea.Model, bubbletea.Cmd) {
	switch msg := msg.(type) {
	case bubbletea.MouseMsg:
		if msg.Type == bubbletea.MouseLeft {
			if msg.Y > 3 && msg.Y <= len(m.choices)+3 {
				m.cursor = msg.Y - 4
				m.mode = m.choices[m.cursor]
				if m.mode == "📤 Send" {
					m.filePrompt = true
					return m, nil
				} else {
					return m, bubbletea.Quit
				}
			}
		}

	case bubbletea.KeyMsg:
		if m.filePrompt {
			if msg.String() == "ctrl+c" {
				return m, bubbletea.Quit
			}
			m.textInput, _ = m.textInput.Update(msg)
			if m.textInput.done {
				m.mode = "📤 Send"
				return m, bubbletea.Quit
			}
			m.suggestions = getPathSuggestions(m.textInput.value)
			switch msg.String() {
			case "tab":
				if len(m.suggestions) > 0 {
					if m.cursor >= len(m.suggestions)-1 {
						m.cursor = 0
					} else {
						m.cursor++
					}
					m.textInput.value = m.suggestions[m.cursor]
				}
			}
			return m, nil
		}

		switch msg.String() {
		case "up", "k":
			if m.cursor > 0 {
				m.cursor--
			}
		case "down", "j":
			if m.cursor < len(m.choices)-1 {
				m.cursor++
			}
		case "g":
			m.cursor = 0
		case "G":
			m.cursor = len(m.choices) - 1
		case "enter":
			if m.filePrompt {
				m.textInput, _ = m.textInput.Update(msg)
				if m.textInput.done {
					m.mode = "📤 Send"
					return m, bubbletea.Quit
				}
				return m, nil
			} else {
				m.mode = m.choices[m.cursor]
				if m.mode == "📤 Send" {
					m.filePrompt = true
					return m, nil
				} else {
					return m, bubbletea.Quit
				}
			}
		case "backspace", "tab":
			if m.filePrompt {
				m.textInput, _ = m.textInput.Update(msg)
				return m, nil
			}
		case "esc":
			if m.filePrompt {
				m.filePrompt = false
				m.textInput = initialTextInputModel()
			}
		default:
			if m.filePrompt {
				m.textInput, _ = m.textInput.Update(msg)
				return m, nil
			}
		}
	}
	return m, nil
}

func (m model) View() string {
	var s strings.Builder

	// 标题
	s.WriteString(titleStyle.Render("💫 LocalSend CLI 💫"))
	s.WriteString("\n\n")

	// 菜单
	if m.mode == "" {
		for i, choice := range m.choices {
			if i == m.cursor {
				s.WriteString(selectedItemStyle.Render(choice))
			} else {
				s.WriteString(unselectedItemStyle.Render(choice))
			}
			s.WriteString("\n")
		}
	} else {
		// 显示当前模式
		s.WriteString(menuStyle.Render(m.mode))
		s.WriteString("\n\n")

		// 文件路径输入
		if m.filePrompt {
			s.WriteString(inputPromptStyle.Render("Enter file path: "))
			s.WriteString(inputStyle.Render(m.textInput.View()))
		}
	}

	return s.String()
}

func WebServerMode(httpServer *http.ServeMux, port int) {
	err := os.MkdirAll("uploads", 0o755)
	if err != nil {
		logger.Errorf("Failed to create uploads directory: %v", err)
		return
	}
	if config.ConfigData.Functions.HttpFileServer {
		httpServer.HandleFunc("/", handlers.IndexFileHandler)
		httpServer.HandleFunc("/uploads/", handlers.FileServerHandler)
		httpServer.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.FS(static.EmbeddedStaticFiles))))
		httpServer.HandleFunc("/send", handlers.NormalSendHandler) // Upload handler
	}
	ips, _ := discovery.GetLocalIP()
	localIP := ""
	for _, ip := range ips {
		ipStr := ip.String()
		if strings.HasPrefix(ipStr, "10.") || strings.HasPrefix(ipStr, "192.168.") {
			logger.Infof("If you opened the HTTP file server, you can view your files on %s", fmt.Sprintf("http://%v:%d", ip, port))
		}
		if strings.HasPrefix(ipStr, "192.168.") {
			localIP = ip.String()
		}
	}
	qr, err := qrcode.New(fmt.Sprintf("http://%s:%d", localIP, port), qrcode.Highest)
	if err != nil {
		fmt.Println("生成二维码失败:", err)
		return
	}

	// 打印二维码到终端
	fmt.Println(qr.ToString(false))
	select {}
}

func ReceiveMode() {
	err := os.MkdirAll("uploads", 0o755)
	if err != nil {
		logger.Errorf("Failed to create uploads directory: %v", err)
		return
	}
	discovery.ListenAndStartBroadcasts(nil)
	logger.Info("Waiting to receive files...")
	select {}
}

func SendMode(filePath string, targetIP string) {
	err := handlers.SendFile(filePath, targetIP)
	if err != nil {
		logger.Errorf("Send failed: %v", err)
	}
}

func ExitMode() {
	fmt.Println("Exiting program...")
	os.Exit(0)
}

func flagParse(httpServer *http.ServeMux, port int, flagOpen *bool) {
	showHelp := func() {
		fmt.Println("Usage: <command> [arguments]")
		fmt.Println("Commands:")
		fmt.Println("  web                 Start Web mode")
		fmt.Println("  send <file_path>    Start Send mode (file path required)")
		fmt.Println("  receive             Start Receive mode")
		fmt.Println("  help                Display this help information")
		fmt.Println("Options:")
		fmt.Println("  --help              Display this help information")
		fmt.Println("  --port=<number>     Specify server port (default: 53317)")
		fmt.Println("  --ip=<ip>           Send directly to the given device IP (skip interactive selection)")
		fmt.Println("  --debug             Enable debug logging (verbose)")
	}
	flag.Usage = showHelp
	// 兼容 --debug 写法（Go flag 仅识别单横线，手动归一化）
	for i, arg := range os.Args {
		if arg == "--debug" {
			os.Args[i] = "-debug"
		}
		if arg == "--port" {
			os.Args[i] = "-port"
		}
	}
	// 解析标准flag参数
	flag.Parse()

	// 手动解析 --ip（兼容 --ip 位于子命令之后，flag.Parse 遇到子命令会停止解析）
	for i, a := range os.Args {
		if strings.HasPrefix(a, "-ip=") || strings.HasPrefix(a, "--ip=") {
			targetIP = a[strings.Index(a, "=")+1:]
			break
		}
		if a == "-ip" || a == "--ip" {
			if i+1 < len(os.Args) {
				targetIP = os.Args[i+1]
			}
			break
		}
	}

	// 应用 --debug 日志级别（flagParse 之后，覆盖 early init，兼容 --debug 在任意位置）
	isDebugFlag := debug
	if !isDebugFlag {
		for _, a := range os.Args {
			if a == "--debug" || a == "-debug" {
				isDebugFlag = true
				break
			}
		}
	}
	if isDebugFlag {
		logger.SetLevel(logrus.DebugLevel)
		logger.Debug("Debug logging enabled via --debug flag")
	}

	// 检查是否有 --help 参数
	for _, arg := range os.Args {
		if arg == "--help" || arg == "-h" {
			showHelp()
			ExitMode()
		}
	}

	// 统一通过过滤 os.Args 获取非 flag 参数，兼容 --debug 在任意位置
	filtered := []string{}
	skipNext := false
	for _, a := range os.Args[1:] {
		if skipNext {
			skipNext = false
			continue
		}
		if a == "-debug" || a == "--debug" {
			continue
		}
		if a == "-h" || a == "--help" {
			continue
		}
		if strings.HasPrefix(a, "-port=") || strings.HasPrefix(a, "--port=") {
			continue
		}
		if a == "-port" || a == "--port" {
			skipNext = true
			continue
		}
		if strings.HasPrefix(a, "-ip=") || strings.HasPrefix(a, "--ip=") {
			continue
		}
		if a == "-ip" || a == "--ip" {
			skipNext = true
			continue
		}
		if strings.HasPrefix(a, "-") {
			// 未知 flag，跳过
			continue
		}
		filtered = append(filtered, a)
	}
	// 也兼容 flag.Args（若用户使用标准 flag 位置）
	args := flag.Args()
	// 优先使用 filtered，若为空则回退到 flag.Args
	positional := filtered
	if len(positional) == 0 && len(args) > 0 {
		positional = args
	}
	if len(positional) > 0 {
		*flagOpen = true
		mode := positional[0]

		switch mode {
		case "web":
			WebServerMode(httpServer, port)
		case "send":
			filePath := ""
			if len(positional) > 1 {
				filePath = positional[1]
				SendMode(filePath, targetIP)
			} else {
				logger.Error("Need file path")
				ExitMode()
			}
		case "receive":
			ReceiveMode()
		case "help":
			showHelp()
			ExitMode()
		default:
			logger.Warnf("Unknown command %q, showing help", mode)
			showHelp()
			ExitMode()
		}
	}
}

var (
	port     int
	debug    bool
	targetIP string
)

func init() {
	flag.IntVar(&port, "port", 53317, "Port to listen on")
	flag.BoolVar(&debug, "debug", false, "Enable debug logging (verbose)")
	flag.StringVar(&targetIP, "ip", "", "Target device IP for send mode (skip interactive selection)")
}

func main() {
	var flagOpen bool = false
	signalChan := make(chan os.Signal, 1)
	signal.Notify(signalChan, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-signalChan
		fmt.Println("\n收到中断信号，正在退出...")
		os.Exit(0)
	}()
	logger.InitLogger()
	// 提早探测 --debug，确保 Server started 之前的 debug 也可见
	for _, a := range os.Args {
		if a == "--debug" || a == "-debug" {
			logger.SetLevel(logrus.DebugLevel)
			break
		}
	}

	// Start HTTP server
	httpServer := server.New()

	/* Send and receive section */
	if config.ConfigData.Functions.LocalSendServer {
		httpServer.HandleFunc("/api/localsend/v2/prepare-upload", handlers.PrepareReceive)
		httpServer.HandleFunc("/api/localsend/v2/upload", handlers.ReceiveHandler)
		httpServer.HandleFunc("/api/localsend/v2/info", handlers.GetInfoHandler)
		httpServer.HandleFunc("/api/localsend/v2/cancel", handlers.HandleCancel)
	}
	go func() {
		logger.Info("Server started at :" + fmt.Sprintf("%d", port))
		if err := http.ListenAndServe(":"+fmt.Sprintf("%d", port), httpServer); err != nil {
			log.Fatalf("Server failed: %v", err)
		}
	}()
	// 参数解析
	flagParse(httpServer, port, &flagOpen)

	if !flagOpen {
		// Run Bubble Tea program
		p := bubbletea.NewProgram(initialModel(), bubbletea.WithoutSignalHandler())
		m, err := p.Run()
		if err != nil {
			log.Fatal(err)
		}

		mTyped := m.(model)
		mode := mTyped.mode

		if mode == "❌ Exit" {
			ExitMode()
		}

		if mode == "📤 Send" {
			filePath := mTyped.textInput.Value()
			if filePath == "" {
				fmt.Println("Send mode requires a file path")
				os.Exit(1)
			}
			SendMode(filePath, "")
		}

		if mode == "📥 Receive" {
			ReceiveMode()
		}
		if mode == "🌎 Web" {
			WebServerMode(httpServer, port)
		}
	}
}
