package httpapi

import (
	"context"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"runtime/debug"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/gin-gonic/gin"

	"itdog-api-golang/internal/itdog"
)

type API struct {
	svc          service
	broker       *itdog.CaptchaBroker
	captchaToken string // 公网部署时保护 /it/captcha*（ITDOG_CAPTCHA_TOKEN）
}

type service interface {
	Ping(ctx context.Context, host string) (map[string]any, error)
	TCPing(ctx context.Context, host string, port int) (map[string]any, error)
	HTTP(ctx context.Context, opt itdog.HTTPOptions) (map[string]any, error)
	DNS(ctx context.Context, domain, dnsType, server string) (map[string]any, error)
}

// captchaProvider 由实现了验证码 broker 的服务提供（可选接口）。
type captchaProvider interface {
	Captcha() *itdog.CaptchaBroker
}

type RouterOption func(*routerConfig)
type routerConfig struct{ requestTimeout time.Duration }

func WithRequestTimeout(timeout time.Duration) RouterOption {
	return func(c *routerConfig) {
		if timeout > 0 {
			c.requestTimeout = timeout
		}
	}
}

func NewRouter(svc service, opts ...RouterOption) *gin.Engine {
	config := routerConfig{requestTimeout: 17 * time.Minute}
	for _, opt := range opts {
		opt(&config)
	}
	g := gin.New()
	g.HandleMethodNotAllowed = true
	g.Use(requestLogger(), recoveryWrapper(), requestLimits(config.requestTimeout))
	g.NoRoute(func(c *gin.Context) { fail(c, http.StatusNotFound, "Not Found") })
	g.NoMethod(func(c *gin.Context) { fail(c, http.StatusMethodNotAllowed, "Method Not Allowed") })

	api := &API{svc: svc, captchaToken: os.Getenv("ITDOG_CAPTCHA_TOKEN")}
	// 健康检查（无鉴权，供容器/LB 探活）
	g.GET("/healthz", func(c *gin.Context) {
		c.String(http.StatusOK, "ok")
	})
	it := g.Group("/it")
	it.GET("/ping", api.ping)
	it.GET("/tcping", api.tcping)
	it.POST("/http", api.httpCheck)
	it.GET("/dns", api.dns)
	if cp, ok := svc.(captchaProvider); ok {
		api.broker = cp.Captcha()
		it.GET("/captcha", api.captchaPage)
		it.GET("/captcha/status", api.captchaStatus)
		it.POST("/captcha", api.captchaSubmit)
	}

	return g
}

func recoveryWrapper() gin.HandlerFunc {
	return func(c *gin.Context) {
		defer func() {
			if r := recover(); r != nil {
				log.Printf("request_id=%s panic_type=%T stack=%s", c.GetString("request_id"), r, debug.Stack())
				if !c.Writer.Written() {
					fail(c, http.StatusInternalServerError, "Internal Server Error")
				} else {
					c.Abort()
				}
			}
		}()
		c.Next()
	}
}

var requestSequence atomic.Uint64

func requestLogger() gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		id := fmt.Sprintf("%x-%x", start.UnixNano(), requestSequence.Add(1))
		c.Set("request_id", id)
		c.Header("X-Request-ID", id)
		defer func() {
			// Query strings, headers, bodies and panic values may contain credentials.
			log.Printf("request_id=%s method=%s path=%q status=%d duration=%s", id, c.Request.Method, c.Request.URL.Path, c.Writer.Status(), time.Since(start))
		}()
		c.Next()
	}
}

func requestLimits(timeout time.Duration) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 128<<10)
		ctx, cancel := context.WithTimeout(c.Request.Context(), timeout)
		defer cancel()
		c.Request = c.Request.WithContext(ctx)
		c.Next()
	}
}

func bindJSON(c *gin.Context, value any) bool {
	decoder := json.NewDecoder(c.Request.Body)
	err := decoder.Decode(value)
	if err == nil {
		var extra any
		err = decoder.Decode(&extra)
		if errors.Is(err, io.EOF) {
			return true
		}
		if err == nil {
			err = errors.New("只接受一个 JSON 对象")
		}
	}
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			fail(c, http.StatusRequestEntityTooLarge, "请求体过大")
		} else {
			fail(c, http.StatusBadRequest, "无效的 JSON 请求")
		}
		return false
	}
	return true
}

func serviceFailure(c *gin.Context, data map[string]any, err error) {
	code := http.StatusBadGateway
	switch {
	case errors.Is(err, itdog.ErrQueueFull), errors.Is(err, itdog.ErrQueueTimeout):
		code = http.StatusServiceUnavailable
		c.Header("Retry-After", "1")
	case errors.Is(err, context.DeadlineExceeded):
		code = http.StatusGatewayTimeout
	case errors.Is(err, context.Canceled):
		code = 499
	}
	envelope := gin.H{"code": code, "timestamp": time.Now().UnixMilli(), "message": err.Error()}
	if data != nil {
		envelope["data"] = data
	}
	c.AbortWithStatusJSON(code, envelope)
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
	data, err := a.svc.Ping(c.Request.Context(), host)
	if err != nil {
		serviceFailure(c, data, err)
		return
	}
	success(c, data)
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
	data, err := a.svc.TCPing(c.Request.Context(), host, port)
	if err != nil {
		serviceFailure(c, data, err)
		return
	}
	success(c, data)
}

func (a *API) httpCheck(c *gin.Context) {
	var req itdog.HTTPOptions
	if !bindJSON(c, &req) {
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
	if err := validateHTTPOptions(req); err != nil {
		fail(c, http.StatusBadRequest, err.Error())
		return
	}
	data, err := a.svc.HTTP(c.Request.Context(), req)
	if err != nil {
		serviceFailure(c, data, err)
		return
	}
	success(c, data)
}

func (a *API) dns(c *gin.Context) {
	domain := strings.TrimSpace(c.Query("domain"))
	dnsType := strings.ToLower(strings.TrimSpace(c.Query("type")))
	server := strings.TrimSpace(c.DefaultQuery("server", "223.5.5.5"))
	if domain == "" {
		fail(c, http.StatusBadRequest, "不正确的主机名或IPV4地址！")
		return
	}
	if !validDNSName(domain) {
		fail(c, http.StatusBadRequest, "不正确的主机名或IPV4地址！")
		return
	}
	if !validDNSType(dnsType) {
		fail(c, http.StatusBadRequest, "无效的解析类型！")
		return
	}
	if !validDNSServer(server) {
		fail(c, http.StatusBadRequest, "无效的 DNS 服务器！")
		return
	}
	data, err := a.svc.DNS(c.Request.Context(), domain, dnsType, server)
	if err != nil {
		serviceFailure(c, data, err)
		return
	}
	success(c, data)
}

func validDNSType(v string) bool {
	switch v {
	case "a", "cname", "mx", "aaaa", "ns", "txt", "ptr", "srv":
		return true
	default:
		return false
	}
}

// ---------------------------------------------------------------------------
// 人工解验证码：页面 /it/captcha，数据 /it/captcha/status，提交 /it/captcha
// ---------------------------------------------------------------------------

func (a *API) captchaPage(c *gin.Context) {
	if !a.captchaAllowed(c) {
		fail(c, http.StatusForbidden, "令牌错误")
		return
	}
	c.Header("Cache-Control", "no-store")
	c.Header("Referrer-Policy", "no-referrer")
	if a.captchaToken != "" {
		http.SetCookie(c.Writer, &http.Cookie{
			Name: "itdog_captcha_token", Value: a.captchaToken, Path: "/it/captcha",
			HttpOnly: true, Secure: c.Request.TLS != nil, SameSite: http.SameSiteStrictMode,
		})
	}
	c.Data(http.StatusOK, "text/html; charset=utf-8", []byte(captchaHTML))
}

func (a *API) captchaStatus(c *gin.Context) {
	if !a.captchaAllowed(c) {
		fail(c, http.StatusForbidden, "令牌错误")
		return
	}
	if a.broker == nil {
		fail(c, http.StatusNotFound, "验证码服务未启用")
		return
	}
	c.Header("Cache-Control", "no-store")
	ch, version, ok := a.broker.Pending()
	if !ok {
		success(c, gin.H{"pending": false})
		return
	}
	success(c, gin.H{
		"pending": true,
		"version": version,
		"attempt": ch.Attempt,
		"width":   ch.Width,
		"height":  ch.Height,
		"image":   "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(ch.Image),
	})
}

func (a *API) captchaSubmit(c *gin.Context) {
	if !a.captchaAllowed(c) {
		fail(c, http.StatusForbidden, "令牌错误")
		return
	}
	if a.broker == nil {
		fail(c, http.StatusNotFound, "验证码服务未启用")
		return
	}
	var req struct {
		Points  [][2]int `json:"points"`
		Version int64    `json:"version"`
	}
	if !bindJSON(c, &req) {
		return
	}
	if len(req.Points) != 2 {
		fail(c, http.StatusBadRequest, "需要两个点击坐标！")
		return
	}
	if err := a.broker.Submit(itdog.CaptchaSolution{Points: req.Points}, req.Version); err != nil {
		code := http.StatusBadRequest
		if err == itdog.ErrChallengeRefreshed {
			code = http.StatusConflict
		}
		fail(c, code, err.Error())
		return
	}
	success(c, gin.H{"ok": true})
}

// captchaAllowed 校验访问令牌（未配置则不限制）。
func (a *API) captchaAllowed(c *gin.Context) bool {
	if a.captchaToken == "" {
		return true
	}
	tok := c.Query("token")
	if tok == "" {
		tok = c.GetHeader("X-Captcha-Token")
	}
	if tok == "" {
		tok, _ = c.Cookie("itdog_captcha_token")
	}
	return subtle.ConstantTimeCompare([]byte(tok), []byte(a.captchaToken)) == 1
}

const captchaHTML = `<!doctype html>
<html lang="zh-CN"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>itdog 人机验证</title><style>
body{font-family:system-ui,sans-serif;background:#f4f5f7;margin:0;padding:24px;color:#222}
.wrap{max-width:420px;margin:0 auto;background:#fff;border-radius:12px;padding:20px;box-shadow:0 2px 12px rgba(0,0,0,.08)}
h1{font-size:17px;margin:0 0 4px}.tip{color:#888;font-size:13px;margin-bottom:12px}
#imgbox{position:relative;line-height:0;cursor:crosshair;border:1px solid #ddd;border-radius:6px;overflow:hidden}
#imgbox img{width:100%;display:block}
.dot{position:absolute;width:18px;height:18px;margin:-9px 0 0 -9px;border-radius:50%;background:rgba(229,72,77,.85);color:#fff;font-size:12px;line-height:18px;text-align:center;transform:scale(var(--s,1))}
button{margin-top:14px;width:100%;padding:11px;border:0;border-radius:8px;background:#2f6fed;color:#fff;font-size:15px;cursor:pointer}
button:disabled{background:#b7c4e4}
#msg{margin-top:10px;font-size:13px;color:#e5484d;min-height:18px}
#status{margin-top:8px;font-size:12px;color:#888}
</style></head><body><div class="wrap">
<h1>itdog 点选验证码</h1>
<div class="tip">请按顺序点击图中要求的 2 个字（不含顶部提示文字），然后提交。</div>
<div id="imgbox"><img id="img" alt="验证码"></div>
<button id="submit" disabled>提交</button>
<div id="msg"></div><div id="status"></div>
</div><script>
var pts=[];var cur=null;
var TOK=new URLSearchParams(location.search).get('token')||'';
if(TOK){var clean=new URL(location.href);clean.searchParams.delete('token');history.replaceState(null,'',clean.pathname+clean.search+clean.hash)}
function th(){return TOK?{'X-Captcha-Token':TOK}:{}}
function setStatus(t){document.getElementById('status').textContent=t}
function load(){
  fetch('/it/captcha/status',{headers:th()}).then(function(r){return r.json()}).then(function(j){
    if(j.code!==200){document.getElementById('msg').textContent=j.message||'加载失败';return}
    var d=j.data;
    if(!d.pending){cur=null;pts=[];document.getElementById('img').removeAttribute('src');document.getElementById('msg').textContent='当前没有需要处理的验证码';document.getElementById('submit').disabled=true;setStatus('等待中…页面会自动刷新');setTimeout(load,3000);return}
    cur=d;pts=[];
    var img=document.getElementById('img');img.src=d.image;
    document.getElementById('msg').textContent='第 '+d.attempt+' 次验证';
    document.getElementById('submit').disabled=true;
    renderDots();setStatus('图片尺寸 '+d.width+'×'+d.height+'（版本 '+d.version+'）');
  }).catch(function(){setTimeout(load,3000)});
}
function renderDots(){
  var box=document.getElementById('imgbox');
  Array.prototype.slice.call(box.querySelectorAll('.dot')).forEach(function(n){n.remove()});
  if(!cur)return;
  var img=document.getElementById('img');
  var s=img.clientWidth/cur.width;
  pts.forEach(function(p,i){
    var d=document.createElement('div');d.className='dot';d.textContent=i+1;
    d.style.left=(p[0]*s)+'px';d.style.top=(p[1]*s)+'px';d.style.setProperty('--s',s);
    box.appendChild(d);
  });
}
document.getElementById('img').addEventListener('click',function(e){
  if(!cur)return;
  var r=this.getBoundingClientRect();
  var x=Math.round((e.clientX-r.left)/r.width*cur.width);
  var y=Math.round((e.clientY-r.top)/r.height*cur.height);
  if(pts.length>=2)pts=[];
  pts.push([x,y]);renderDots();
  document.getElementById('submit').disabled=pts.length!==2;
});
document.getElementById('submit').addEventListener('click',function(){
  if(pts.length!==2||!cur)return;
  fetch('/it/captcha',{method:'POST',headers:Object.assign({'Content-Type':'application/json'},th()),body:JSON.stringify({points:pts,version:cur.version})})
    .then(function(r){return r.json()}).then(function(j){
      if(j.code===200){document.getElementById('msg').textContent='已提交，正在继续任务…';setTimeout(load,1500)}
      else{document.getElementById('msg').textContent=j.message||'提交失败';setTimeout(load,1500)}
    });
});
window.addEventListener('resize',renderDots);
load();
</script></body></html>`

func success(c *gin.Context, data any) {
	c.JSON(http.StatusOK, gin.H{
		"code":      200,
		"timestamp": time.Now().UnixMilli(),
		"data":      data,
	})
}

func fail(c *gin.Context, code int, message string) {
	c.AbortWithStatusJSON(code, gin.H{
		"code":      code,
		"timestamp": time.Now().UnixMilli(),
		"message":   message,
	})
}
