package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
)

const indexPath = "/jwglxt/kbcx/xskbcx_cxXskbcxIndex.html?gnmkdm=N2151&layout=default"

type browser struct {
	ctx  context.Context
	base string
}

func startBrowser(parent context.Context, base, executable string, headless bool) (*browser, func(), error) {
	profile, err := os.MkdirTemp("", "nbt-export-browser-")
	if err != nil {
		return nil, nil, err
	}
	opts := append([]chromedp.ExecAllocatorOption{}, chromedp.DefaultExecAllocatorOptions[:]...)
	opts = append(opts, chromedp.UserDataDir(profile), chromedp.Flag("headless", headless))
	if executable != "" {
		opts = append(opts, chromedp.ExecPath(executable))
	}
	alloc, stopAlloc := chromedp.NewExecAllocator(parent, opts...)
	ctx, stop := chromedp.NewContext(alloc)
	cleanup := func() { stop(); stopAlloc(); _ = os.RemoveAll(profile) }
	if err := chromedp.Run(ctx); err != nil {
		cleanup()
		return nil, nil, fmt.Errorf("无法启动 Chrome/Chromium；请安装浏览器或使用 --browser 指定路径：%w", err)
	}
	return &browser{ctx: ctx, base: base}, cleanup, nil
}

func jsValue(v any) string { b, _ := json.Marshal(v); return string(b) }

// Only fill passwords on campus HTTPS pages, never on an arbitrary redirect.
// The site's own submit handler performs CAS AES or JWXT RSA encryption.
func loginScript(user, password string) string {
	return `(() => {
  if (location.protocol !== 'https:' || !(location.hostname === 'nbt.edu.cn' || location.hostname.endsWith('.nbt.edu.cn'))) return 'untrusted';
  const visible = e => e && e.getClientRects().length > 0;
  const tab = document.querySelector('#userNameLogin_a');
  if (visible(tab) && !visible(document.querySelector('#pwdFromId #password'))) { tab.click(); return 'switching'; }
  const form = document.querySelector('#pwdFromId') || document.querySelector('#fm1') || document.querySelector('#casLoginForm') || document.querySelector('#loginForm');
  const root = form || document;
  const u = [...root.querySelectorAll('input[name="username"],input[name="yhm"],#yhm')].find(visible);
  const p = [...root.querySelectorAll('input[type="password"]')].find(visible);
  const button = root.querySelector('#login_submit') || root.querySelector('#dl') || root.querySelector('button[type="submit"],input[type="submit"]');
  if (!u || !p || !button) return 'manual';
  if (window.__nbtExportSubmitted) return 'waiting';
  const fill = (e,v) => { Object.getOwnPropertyDescriptor(HTMLInputElement.prototype,'value').set.call(e,v); e.dispatchEvent(new Event('input',{bubbles:true})); e.dispatchEvent(new Event('change',{bubbles:true})); };
  if (!window.__nbtExportFilled) {
    fill(u, ` + jsValue(user) + `); fill(p, ` + jsValue(password) + `);
    u.dispatchEvent(new Event('blur')); u.dispatchEvent(new FocusEvent('focusout',{bubbles:true}));
    window.__nbtExportFilled = true; window.__nbtExportReadyAt = Date.now() + 1800; return 'filled';
  }
  if (Date.now() < window.__nbtExportReadyAt) return 'waiting';
  window.__nbtExportSubmitted = true; button.click(); return 'submitted';
})()`
}

// Wait for an authenticated timetable page rather than assuming a successful
// login POST. A visible browser allows manual captcha/MFA without bypasses.
func (b *browser) login(user, password string, timeout time.Duration) (string, error) {
	ctx, cancel := context.WithTimeout(b.ctx, timeout)
	defer cancel()
	if err := chromedp.Run(ctx, chromedp.Navigate(b.base+indexPath)); err != nil {
		return "", fmt.Errorf("无法打开教务系统；校外请用 --site vpn，校内可试 --site direct：%w", err)
	}
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	var lastAttempt, lastNavigation string
	for {
		select {
		case <-ctx.Done():
			return "", errors.New("登录等待超时；请确认账号密码，并在浏览器完成验证码或二次认证；可增加 --login-timeout")
		case <-ticker.C:
			var state struct {
				URL   string `json:"url"`
				Ready bool   `json:"ready"`
				HTML  string `json:"html"`
			}
			err := chromedp.Run(ctx, chromedp.Evaluate(`({url:location.href,ready:!!document.querySelector('select#xnm,select[name="xnm"]')&&!!document.querySelector('select#xqm,select[name="xqm"]'),html:document.documentElement.outerHTML})`, &state))
			if err != nil {
				continue
			} // A redirect may destroy the execution context.
			u, err := url.Parse(state.URL)
			if err != nil {
				continue
			}
			if state.Ready && u.Scheme+"://"+u.Host == b.base {
				return state.HTML, nil
			}
			// VPN authentication can end at its portal; continue to the original target.
			if (u.Hostname() == "webvpn.nbt.edu.cn" && (u.Path == "/" || u.Path == "/portal")) ||
				(u.Scheme+"://"+u.Host == b.base && strings.Contains(u.Path, "/xtgl/index")) {
				if lastNavigation != state.URL {
					lastNavigation = state.URL
					_ = chromedp.Run(ctx, chromedp.Navigate(b.base+indexPath))
				}
				continue
			}
			if user != "" && password != "" {
				// No automatic re-submission on a failed login reload (avoids lockouts).
				key := u.Host + u.Path
				if lastAttempt != key {
					var status string
					if chromedp.Run(ctx, chromedp.Evaluate(loginScript(user, password), &status)) == nil && status == "submitted" {
						lastAttempt = key
					}
				}
			}
		}
	}
}

func (b *browser) fetch(path string, form url.Values) ([]byte, error) {
	ctx, cancel := context.WithTimeout(b.ctx, 40*time.Second)
	defer cancel()
	var result struct {
		Status int    `json:"status"`
		Body   string `json:"body"`
		URL    string `json:"url"`
		Error  string `json:"error"`
	}
	script := `(async () => { try {
 if (location.origin !== ` + jsValue(b.base) + `) return {error:'教务页面已离开原站点，请重新登录'};
 const r = await fetch(` + jsValue(path) + `, {method:'POST',credentials:'same-origin',headers:{'Content-Type':'application/x-www-form-urlencoded;charset=UTF-8','X-Requested-With':'XMLHttpRequest'},body:` + jsValue(form.Encode()) + `,signal:AbortSignal.timeout(30000)});
 return {status:r.status,body:await r.text(),url:r.url};
 } catch(e) { return {error:'请求失败，请检查网络或重新登录'}; } })()`
	if err := chromedp.Run(ctx, chromedp.Evaluate(script, &result, func(p *runtime.EvaluateParams) *runtime.EvaluateParams { return p.WithAwaitPromise(true) })); err != nil {
		return nil, fmt.Errorf("读取课表失败：%w", err)
	}
	if result.Error != "" {
		return nil, errors.New(result.Error)
	}
	if result.Status < 200 || result.Status >= 300 {
		return nil, fmt.Errorf("教务系统返回 HTTP %d", result.Status)
	}
	if strings.Contains(result.URL, "/authserver/") || strings.Contains(result.Body, "pwdEncryptSalt") {
		return nil, errors.New("登录已过期，请重新运行")
	}
	return []byte(result.Body), nil
}
