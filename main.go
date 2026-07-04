package main

import (
	"database/sql"
	"encoding/json"
	"encoding/xml"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	_ "github.com/mattn/go-sqlite3"
	"gopkg.in/yaml.v3"
)

// ==================== 配置结构 ====================

type Config struct {
	Server struct {
		Port              int `yaml:"port"`
		ClientMaxBodySize int `yaml:"client_max_body_size"`
		ReadTimeout       int `yaml:"read_timeout"`
		WriteTimeout      int `yaml:"write_timeout"`
	} `yaml:"server"`
	Navidrome struct {
		URL                string `yaml:"url"`
		MusicLibraryPath   string `yaml:"music_library_path"`
	} `yaml:"navidrome"`
	PS2115 struct {
		DBPath      string `yaml:"db_path"`
		RedirectURL string `yaml:"redirect_url"`
	} `yaml:"ps2115"`
	Cache struct {
		Enabled bool `yaml:"enabled"`
		TTL     int  `yaml:"ttl"`
	} `yaml:"cache"`
	Log struct {
		Level  string `yaml:"level"`
		Format string `yaml:"format"`
	} `yaml:"log"`
}

// ==================== 缓存结构 ====================

type CacheEntry struct {
	URL       string
	Timestamp time.Time
	TTL       time.Duration
}

func (e *CacheEntry) IsExpired() bool {
	return time.Since(e.Timestamp) > e.TTL
}

type URLCache struct {
	mu       sync.RWMutex
	cache    map[string]*CacheEntry
	ttl      time.Duration
	stopChan chan struct{}
}

func NewURLCache(ttl time.Duration) *URLCache {
	uc := &URLCache{
		cache:    make(map[string]*CacheEntry),
		ttl:      ttl,
		stopChan: make(chan struct{}),
	}
	go uc.cleanup()
	return uc
}

func (uc *URLCache) Set(key string, url string) {
	uc.mu.Lock()
	defer uc.mu.Unlock()

	uc.cache[key] = &CacheEntry{
		URL:       url,
		Timestamp: time.Now(),
		TTL:       uc.ttl,
	}
}

func (uc *URLCache) Get(key string) string {
	uc.mu.RLock()
	defer uc.mu.RUnlock()

	if entry, ok := uc.cache[key]; ok {
		if !entry.IsExpired() {
			return entry.URL
		}
	}
	return ""
}

func (uc *URLCache) Delete(key string) {
	uc.mu.Lock()
	defer uc.mu.Unlock()
	delete(uc.cache, key)
}

func (uc *URLCache) Clear() {
	uc.mu.Lock()
	defer uc.mu.Unlock()
	uc.cache = make(map[string]*CacheEntry)
}

func (uc *URLCache) cleanup() {
	ticker := time.NewTicker(1 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			uc.removeExpired()
		case <-uc.stopChan:
			return
		}
	}
}

func (uc *URLCache) removeExpired() {
	uc.mu.Lock()
	defer uc.mu.Unlock()

	now := time.Now()
	for key, entry := range uc.cache {
		if now.Sub(entry.Timestamp) > entry.TTL {
			delete(uc.cache, key)
		}
	}
}

// ==================== 全局变量 ====================

var (
	cfg        *Config
	httpClient *http.Client
	cache      *URLCache
	dataDir    string // 数据目录路径
	logFile    *os.File
)

// ==================== API 响应结构 ====================

type NavidromeSongResponse struct {
	SubsonicResponse struct {
		Song struct {
			Path string `json:"path"`
		} `json:"song"`
	} `json:"subsonic-response"`
}

type NavidromeSongResponseXML struct {
	Song struct {
		Path string `xml:"path,attr"`
	} `xml:"song"`
}

// ==================== 日志初始化 ====================

// cleanOldLogs 清理3天前的日志文件
func cleanOldLogs(logDir string, keepDays int) error {
	entries, err := os.ReadDir(logDir)
	if err != nil {
		return fmt.Errorf("无法读取日志目录: %v", err)
	}

	now := time.Now()
	cutoffTime := now.AddDate(0, 0, -keepDays)

	for _, entry := range entries {
		// 只处理以 nav-proxy- 开头且以 .log 结尾的文件
		if !entry.IsDir() && strings.HasPrefix(entry.Name(), "nav-proxy-") && strings.HasSuffix(entry.Name(), ".log") {
			info, err := entry.Info()
			if err != nil {
				log.Printf("警告：无法获取文件信息 %s: %v", entry.Name(), err)
				continue
			}

			// 如果文件修改时间早于cutoffTime，则删除
			if info.ModTime().Before(cutoffTime) {
				filePath := filepath.Join(logDir, entry.Name())
				if err := os.Remove(filePath); err != nil {
					log.Printf("警告：无法删除旧日志文件 %s: %v", filePath, err)
				} else {
					log.Printf("已删除旧日志文件: %s", entry.Name())
				}
			}
		}
	}

	return nil
}

func initLogger(logDir string) error {
	// 创建日志目录
	if err := os.MkdirAll(logDir, 0755); err != nil {
		return fmt.Errorf("无法创建日志目录: %v", err)
	}

	// 清理3天前的日志文件
	if err := cleanOldLogs(logDir, 3); err != nil {
		return err
	}

	// 日志文件路径
	logPath := filepath.Join(logDir, fmt.Sprintf("navi-proxy-%s.log", time.Now().Format("2006-01-02")))

	// 打开日志文件
	var err error
	logFile, err = os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return fmt.Errorf("无法打开日志文件: %v", err)
	}

	// 创建多输出日志：既输出到文件也输出到 stdout
	multiWriter := io.MultiWriter(os.Stdout, logFile)
	log.SetOutput(multiWriter)
	log.SetFlags(log.LstdFlags | log.Lshortfile)

	log.Printf("日志已初始化: %s", logPath)
	return nil
}

// ==================== 配置加载 ====================

func loadConfig(configPath string) error {
	// 加载配置文件
	data, err := os.ReadFile(configPath)
	if err != nil {
		return fmt.Errorf("无法读取配置文件 %s: %v", configPath, err)
	}

	cfg = &Config{}
	if err := yaml.Unmarshal(data, cfg); err != nil {
		return fmt.Errorf("无法解析配置文件: %v", err)
	}

	log.Printf("✓ 配置文件已加载: %s", configPath)
	log.Printf("  - Navidrome: %s", cfg.Navidrome.URL)
	log.Printf("  - PS2115 DB: %s", cfg.PS2115.DBPath)
	log.Printf("  - Cache: %v (TTL: %ds)", cfg.Cache.Enabled, cfg.Cache.TTL)

	// 初始化HTTP客户端
	httpClient = &http.Client{
		Timeout: 30 * time.Second,
	}

	// 初始化缓存
	cache = NewURLCache(time.Duration(cfg.Cache.TTL) * time.Second)

	// 设置日志级别
	if cfg.Log.Level == "debug" {
		gin.SetMode(gin.DebugMode)
		log.Printf("日志级别: DEBUG")
	} else {
		gin.SetMode(gin.ReleaseMode)
		log.Printf("日志级别: INFO")
	}

	return nil
}

// ==================== 处理函数 ====================

// getNavidromeMusicPathWithQuery 从Navidrome获取音乐文件路径 (使用完整query参数)
func getNavidromeMusicPathWithQuery(id string, rawQuery string, c *gin.Context) (string, error) {
	// 解析原始query参数，并用getSong替换操作
	getSongURL := fmt.Sprintf("%s/rest/getSong?%s", cfg.Navidrome.URL, rawQuery)

	log.Printf("请求Navidrome: %s", getSongURL)

	// 创建请求
	req, err := http.NewRequest("GET", getSongURL, nil)
	if err != nil {
		return "", fmt.Errorf("无法创建请求: %v", err)
	}

	// 设置User-Agent和其他头
	req.Header.Set("User-Agent", c.GetHeader("User-Agent"))
	if req.Header.Get("User-Agent") == "" {
		req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36")
	}
	req.Header.Set("Accept-Language", "zh-CN,zh-Hans;q=0.9")

	// 执行请求
	resp, err := httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("无法连接到Navidrome: %v", err)
	}
	defer resp.Body.Close()

	// 读取响应体
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("无法读取响应: %v", err)
	}

	return extractPathFromResponse(body)
}

// extractPathFromResponse 从Navidrome响应中提取路径
func extractPathFromResponse(body []byte) (string, error) {
	responseText := string(body)
	log.Printf("Navidrome响应: %s", responseText)

	// 尝试解析为JSON
	var jsonResp NavidromeSongResponse
	if err := json.Unmarshal(body, &jsonResp); err == nil {
		if jsonResp.SubsonicResponse.Song.Path != "" {
			return jsonResp.SubsonicResponse.Song.Path, nil
		}
	}

	// 尝试解析为XML
	var xmlResp NavidromeSongResponseXML
	if err := xml.Unmarshal(body, &xmlResp); err == nil {
		if xmlResp.Song.Path != "" {
			return xmlResp.Song.Path, nil
		}
	}

	// 尝试正则表达式提取路径
	re := regexp.MustCompile(`path="([^"]+)"`)
	matches := re.FindStringSubmatch(responseText)
	if len(matches) > 1 {
		return matches[1], nil
	}

	return "", fmt.Errorf("无法从响应中提取路径")
}

// getNavidromeMusicPath 从Navidrome获取音乐文件路径
func getNavidromeMusicPath(id string, c *gin.Context) (string, error) {
	// 构建Navidrome API URL
	getSongURL := fmt.Sprintf("%s/rest/getSong?id=%s", cfg.Navidrome.URL, id)

	log.Printf("请求Navidrome: %s", getSongURL)

	// 创建请求
	req, err := http.NewRequest("GET", getSongURL, nil)
	if err != nil {
		return "", fmt.Errorf("无法创建请求: %v", err)
	}

	// 设置User-Agent和其他头
	req.Header.Set("User-Agent", c.GetHeader("User-Agent"))
	if req.Header.Get("User-Agent") == "" {
		req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36")
	}
	req.Header.Set("Accept-Language", "zh-CN,zh-Hans;q=0.9")

	// 执行请求
	resp, err := httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("无法连接到Navidrome: %v", err)
	}
	defer resp.Body.Close()

	// 读取响应体
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("无法读取响应: %v", err)
	}

	return extractPathFromResponse(body)
}

// getPickcodeFromDB 从ps2115数据库查询pickcode
func getPickcodeFromDB(musicRelativePath string) (string, string, error) {
	// 打开数据库
	db, err := sql.Open("sqlite3", cfg.PS2115.DBPath)
	if err != nil {
		return "", "", fmt.Errorf("无法打开数据库: %v", err)
	}
	defer db.Close()

	// 清理路径，移除开头的 /music
	relativePath := strings.TrimPrefix(musicRelativePath, cfg.Navidrome.MusicLibraryPath)
	relativePath = strings.TrimPrefix(relativePath, "/")
	
	log.Printf("查询数据库中的相对路径: %s", relativePath)

	// 从路径中提取parent_path和file_name
	// 例如: "林忆莲/许愿创作制作歌集/04. 大地.m4a" -> parent_path: "林忆莲/许愿创作制作歌集", file_name: "04. 大地.m4a"
	lastSlashIndex := strings.LastIndex(relativePath, "/")
	var parentPath, fileName string
	
	if lastSlashIndex > 0 {
		parentPath = relativePath[:lastSlashIndex]
		fileName = relativePath[lastSlashIndex+1:]
	} else if lastSlashIndex == 0 {
		parentPath = ""
		fileName = relativePath[1:]
	} else {
		// 没有斜杠，整个是文件名，parent_path为空
		parentPath = ""
		fileName = relativePath
	}
	
	log.Printf("分解后的路径 - parent_path: '%s', file_name: '%s'", parentPath, fileName)

	// 查询数据库
	// ps2115数据库中strm_index表的结构:
	// parent_path (TEXT) - 父目录路径
	// file_name (TEXT) - 文件名
	// pick_code (TEXT) - 114网盘的pick_code
	query := `
		SELECT pick_code, file_name
		FROM strm_index 
		WHERE parent_path = ? AND file_name = ?
		LIMIT 1
	`
	
	var pickcode, filename string
	err = db.QueryRow(query, parentPath, fileName).Scan(&pickcode, &filename)
	if err != nil {
		if err == sql.ErrNoRows {
			// 精确查询失败，尝试模糊查询
			log.Printf("精确查询失败，尝试模糊查询")
			query = `
				SELECT pick_code, file_name
				FROM strm_index 
				WHERE (parent_path LIKE ? OR parent_path LIKE ?) 
				  AND file_name LIKE ?
				LIMIT 1
			`
			// 尝试匹配父路径的模糊版本
			parentPattern := "%" + parentPath
			parentPattern2 := parentPath + "%"
			filePattern := "%" + fileName
			
			err = db.QueryRow(query, parentPattern, parentPattern2, filePattern).Scan(&pickcode, &filename)
			if err != nil {
				if err == sql.ErrNoRows {
					return "", "", fmt.Errorf("数据库中未找到文件: %s", relativePath)
				}
				return "", "", fmt.Errorf("查询数据库错误: %v", err)
			}
		} else {
			return "", "", fmt.Errorf("查询数据库错误: %v", err)
		}
	}

	if pickcode == "" {
		return "", "", fmt.Errorf("获取到的pickcode为空")
	}

	log.Printf("成功查询到 - pickcode: %s, file_name: %s", pickcode, filename)
	return pickcode, filename, nil
}

// corsMiddleware 处理CORS请求
func corsMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Writer.Header().Set("Access-Control-Allow-Origin", "*")
		c.Writer.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		c.Writer.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, X-Requested-With")
		c.Writer.Header().Set("Access-Control-Allow-Credentials", "true")
		c.Writer.Header().Set("Access-Control-Max-Age", "3600")
		c.Writer.Header().Set("Referrer-Policy", "no-referrer")

		if c.Request.Method == "OPTIONS" {
			c.AbortWithStatus(204)
			return
		}

		c.Next()
	}
}

// logMiddleware 记录请求日志
func logMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		startTime := time.Now()
		path := c.Request.URL.Path
		method := c.Request.Method

		c.Next()

		duration := time.Since(startTime)
		statusCode := c.Writer.Status()

		if cfg.Log.Level == "debug" {
			log.Printf("[%s] %s %s - %d (%v)", method, path, c.Request.URL.RawQuery, statusCode, duration)
		}
	}
}

// streamRedirectHandler 处理音频流重定向请求 (反代劫持逻辑)
func streamRedirectHandler(c *gin.Context) {
	// 获取id参数 (从query中获取)
	id := c.Query("id")
	if id == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "缺少id参数"})
		return
	}

	log.Printf("处理音频流请求: id=%s", id)

	// 检查缓存
	if cfg.Cache.Enabled {
		if cachedURL := cache.Get(id); cachedURL != "" {
			log.Printf("缓存命中，使用缓存URL")
			c.Redirect(http.StatusFound, cachedURL)
			return
		}
	}

	// 获取音乐文件路径 (使用原始query参数)
	path, err := getNavidromeMusicPathWithQuery(id, c.Request.URL.RawQuery, c)
	if err != nil {
		log.Printf("获取音乐路径失败: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	if path == "" {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "无法获取音乐文件路径"})
		return
	}

	log.Printf("获取到音乐文件路径: %s", path)

	// 从数据库查询pickcode而不是调用Alist API
	pickcode, filename, err := getPickcodeFromDB(path)
	if err != nil {
		log.Printf("查询数据库失败: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	if pickcode == "" {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "无法获取pickcode"})
		return
	}

	log.Printf("获取到pickcode: %s", pickcode)

	// 构建直链URL
	// 格式: http://192.168.32.236:8080/api/strm302/redirect?pickcode={pickcode}&file_name={filename}
	directURL := fmt.Sprintf("%spickcode=%s&file_name=%s", 
		cfg.PS2115.RedirectURL, 
		url.QueryEscape(pickcode), 
		url.QueryEscape(filename))

	log.Printf("构建的直链URL: %s", directURL)

	// 缓存URL
	if cfg.Cache.Enabled {
		cache.Set(id, directURL)
	}

	// 重定向到直链
	c.Redirect(http.StatusFound, directURL)
}

// proxyHandler 通用代理处理器
func proxyHandler(c *gin.Context) {
	// 处理音频流重定向请求 (支持 /rest/stream 和 /rest/stream.view)
	path := c.Request.URL.Path
	if path == "/rest/stream" || path == "/rest/stream.view" || strings.HasPrefix(path, "/rest/stream?") {
		streamRedirectHandler(c)
		return
	}

	// 构建目标URL
	targetURL := cfg.Navidrome.URL + c.Request.RequestURI

	req, err := http.NewRequest(c.Request.Method, targetURL, c.Request.Body)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "无法创建请求"})
		return
	}

	// 复制请求头
	for key, values := range c.Request.Header {
		for _, value := range values {
			req.Header.Add(key, value)
		}
	}

	// 执行请求
	resp, err := httpClient.Do(req)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "无法连接到Navidrome"})
		return
	}
	defer resp.Body.Close()

	// 复制响应头
	for key, values := range resp.Header {
		for _, value := range values {
			c.Header(key, value)
		}
	}

	// 复制响应体
	c.Status(resp.StatusCode)
	if _, err := io.Copy(c.Writer, resp.Body); err != nil {
		log.Printf("复制响应体失败: %v", err)
	}
}

// ==================== 主函数 ====================

func main() {
	// 解析命令行参数
	flag.StringVar(&dataDir, "d", "./data", "数据目录路径（包含config.yaml和日志）")
	flag.Parse()

	// 确保数据目录存在
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		log.Fatalf("无法创建数据目录 %s: %v", dataDir, err)
	}

	// 初始化日志系统
	if err := initLogger(dataDir); err != nil {
		log.Fatalf("无法初始化日志系统: %v", err)
	}
	defer func() {
		if logFile != nil {
			logFile.Close()
		}
	}()

	// 加载配置文件
	configPath := filepath.Join(dataDir, "config.yaml")
	if err := loadConfig(configPath); err != nil {
		log.Fatalf("配置加载失败: %v", err)
	}

	router := gin.Default()

	// 添加中间件
	router.Use(corsMiddleware())
	router.Use(logMiddleware())

	// 代理路由（会在内部处理/rest/stream）
	router.Any("/*proxyPath", func(c *gin.Context) {
		// 检查是否是流请求 - 支持多种模式
		path := c.Request.URL.Path
		if isStreamRequest(path) {
			streamRedirectHandler(c)
		} else {
			proxyHandler(c)
		}
	})

	// 启动服务器
	addr := fmt.Sprintf(":%d", cfg.Server.Port)
	log.Printf("╔════════════════════════════════════════╗")
	log.Printf("║   Navidrome 反代劫持服务启动成功        ║")
	log.Printf("║   数据目录: %s", dataDir)
	log.Printf("║   监听地址: %s                        ║", addr)
	log.Printf("╚════════════════════════════════════════╝")

	server := &http.Server{
		Addr:         addr,
		Handler:      router,
		ReadTimeout:  time.Duration(cfg.Server.ReadTimeout) * time.Second,
		WriteTimeout: time.Duration(cfg.Server.WriteTimeout) * time.Second,
	}

	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("服务器启动失败: %v", err)
	}
}

// isStreamRequest 判断是否是流请求
func isStreamRequest(path string) bool {
	// 使用正则表达式匹配 /rest/stream 相关路由
	// 支持: /rest/stream, /rest/stream.view, /rest/stream.raw 等
	pattern := regexp.MustCompile(`^/rest/stream($|\.|\?)`)
	
	// 解析URL以获取查询参数
	if u, err := url.Parse(path); err == nil {
		if pattern.MatchString(u.Path) {
			return true
		}
	}
	
	// 直接检查路径
	if strings.Contains(path, "/rest/stream") {
		// 确保这是一个完整的/rest/stream请求，而不是其他以此开头的路由
		if strings.HasPrefix(path, "/rest/stream") {
			// 检查后面是什么
			remainder := strings.TrimPrefix(path, "/rest/stream")
			if remainder == "" || remainder[0] == '.' || remainder[0] == '?' || remainder[0] == ';' {
				return true
			}
		}
	}
	
	return false
}
