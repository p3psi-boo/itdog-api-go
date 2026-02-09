package httpapi

import (
	"context"
	"log"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"itdog-api-golang/internal/itdog"
)

type API struct {
	svc service
}

type service interface {
	Ping(ctx context.Context, host string) map[string]any
	TCPing(ctx context.Context, host string, port int) map[string]any
	HTTP(ctx context.Context, opt itdog.HTTPOptions) map[string]any
	DNS(ctx context.Context, domain, dnsType, server string) map[string]any
}

func NewRouter(svc service) *gin.Engine {
	g := gin.New()
	g.Use(recoveryWrapper())
	g.Use(requestLogger())
	g.NoRoute(func(c *gin.Context) { fail(c, http.StatusNotFound, "Not Found") })
	g.NoMethod(func(c *gin.Context) { fail(c, http.StatusMethodNotAllowed, "Method Not Allowed") })

	api := &API{svc: svc}
	it := g.Group("/it")
	it.GET("/ping", api.ping)
	it.GET("/tcping", api.tcping)
	it.POST("/http", api.httpCheck)
	it.GET("/dns", api.dns)

	return g
}

func recoveryWrapper() gin.HandlerFunc {
	return func(c *gin.Context) {
		defer func() {
			if r := recover(); r != nil {
				fail(c, http.StatusInternalServerError, "Internal Server Error")
			}
		}()
		c.Next()
	}
}

func requestLogger() gin.HandlerFunc {
	return func(c *gin.Context) {
		log.Printf("[%s] %s - %s", c.Request.Method, c.Request.URL.String(), c.GetHeader("User-Agent"))
		c.Next()
	}
}

func (a *API) ping(c *gin.Context) {
	host := strings.TrimSpace(c.Query("host"))
	if host == "" {
		fail(c, http.StatusBadRequest, "不正确的主机名或IPV4地址！")
		return
	}
	if !validHost(host) {
		fail(c, http.StatusBadRequest, "不正确的主机名或IPV4地址！")
		return
	}
	success(c, a.svc.Ping(c.Request.Context(), host))
}

func (a *API) tcping(c *gin.Context) {
	host := strings.TrimSpace(c.Query("host"))
	portStr := strings.TrimSpace(c.Query("port"))
	if host == "" || !validHost(host) {
		fail(c, http.StatusBadRequest, "不正确的主机名或IPV4地址！")
		return
	}
	port, err := strconv.Atoi(portStr)
	if err != nil || port < 1 || port > 65535 {
		fail(c, http.StatusBadRequest, "无效的端口号！")
		return
	}
	success(c, a.svc.TCPing(c.Request.Context(), host, port))
}

func (a *API) httpCheck(c *gin.Context) {
	var req itdog.HTTPOptions
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "无效的请求链接！")
		return
	}
	if !validURL(req.URL) {
		fail(c, http.StatusBadRequest, "无效的请求链接！")
		return
	}
	method := strings.ToLower(strings.TrimSpace(req.Method))
	if method != "" && method != "get" && method != "post" {
		fail(c, http.StatusBadRequest, "无效的请求方式！")
		return
	}
	req.Method = method
	success(c, a.svc.HTTP(c.Request.Context(), req))
}

func (a *API) dns(c *gin.Context) {
	domain := strings.TrimSpace(c.Query("domain"))
	dnsType := strings.ToLower(strings.TrimSpace(c.Query("type")))
	server := strings.TrimSpace(c.DefaultQuery("server", "223.5.5.5"))
	if domain == "" {
		fail(c, http.StatusBadRequest, "不正确的主机名或IPV4地址！")
		return
	}
	if !validHost(domain) {
		fail(c, http.StatusBadRequest, "不正确的主机名或IPV4地址！")
		return
	}
	if !validDNSType(dnsType) {
		fail(c, http.StatusBadRequest, "无效的解析类型！")
		return
	}
	success(c, a.svc.DNS(c.Request.Context(), domain, dnsType, server))
}

func validHost(host string) bool {
	ip := net.ParseIP(host)
	if ip != nil && ip.To4() != nil {
		return true
	}
	addrs, err := net.LookupIP(host)
	return err == nil && len(addrs) > 0
}

func validURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	return u.Scheme != "" && u.Host != ""
}

func validDNSType(v string) bool {
	switch v {
	case "a", "cname", "mx", "aaaa", "ns", "txt", "ptr", "srv":
		return true
	default:
		return false
	}
}

func success(c *gin.Context, data any) {
	c.JSON(http.StatusOK, gin.H{
		"code":      200,
		"timestamp": time.Now().UnixMilli(),
		"data":      data,
	})
}

func fail(c *gin.Context, code int, message string) {
	c.JSON(http.StatusOK, gin.H{
		"code":      code,
		"timestamp": time.Now().UnixMilli(),
		"message":   message,
	})
}
