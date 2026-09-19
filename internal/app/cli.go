package app

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"golang.org/x/term"
)

type options struct {
	Site, User, Password, Term, WeekOne, Output, Periods, Browser string
	Headless, ListTerms, Manual, Force                            bool
	LoginTimeout                                                  time.Duration
}

func parseOptions(args []string, out io.Writer) (options, error) {
	o := options{}
	f := flag.NewFlagSet("nbt-export", flag.ContinueOnError)
	f.SetOutput(out)
	f.StringVar(&o.Site, "site", "vpn", "入口：vpn / direct / 两个教务系统的完整 HTTPS 地址")
	f.StringVar(&o.User, "user", "", "学号；也可使用 NBT_USER")
	f.StringVar(&o.Password, "password", "", "密码；建议交互输入或 NBT_PASSWORD")
	f.StringVar(&o.Term, "term", "", "学期，例如 2026-1 或实际代码 2026:3；默认当前学期")
	f.StringVar(&o.WeekOne, "week-one", "", "教学第一周周一（YYYY-MM-DD）；没有权威日期时需要")
	f.StringVar(&o.Output, "output", "个人课表.ics", "输出路径")
	f.StringVar(&o.Periods, "periods", "", "节次时间 JSON 文件，补充/覆盖教务返回时间")
	f.StringVar(&o.Browser, "browser", "", "Chrome / Chromium 可执行文件路径")
	f.BoolVar(&o.Headless, "headless", false, "隐藏浏览器（遇到验证码时请关闭此选项）")
	f.BoolVar(&o.Manual, "manual-login", false, "在浏览器手动登录，不询问账号密码")
	f.BoolVar(&o.ListTerms, "list-terms", false, "登录后列出可选学期并退出")
	f.BoolVar(&o.Force, "force", false, "覆盖已有输出文件")
	f.DurationVar(&o.LoginTimeout, "login-timeout", 5*time.Minute, "登录等待上限，例如 5m")
	f.Usage = func() {
		fmt.Fprint(out, "个人课表导出（Chrome/Chromium + Go）\n\n用法：\n  go run main.go\n  go run main.go 学号 密码 [选项]\n  go run main.go --user 学号 --site direct\n\n")
		f.PrintDefaults()
	}
	// Accept options before or after the two positional credentials.
	var positional, flags []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			positional = append(positional, args[i+1:]...)
			break
		}
		if strings.HasPrefix(a, "-") && a != "-" {
			flags = append(flags, a)
			name := strings.TrimLeft(strings.SplitN(a, "=", 2)[0], "-")
			if !strings.Contains(a, "=") {
				if entry := f.Lookup(name); entry != nil {
					if b, ok := entry.Value.(interface{ IsBoolFlag() bool }); !ok || !b.IsBoolFlag() {
						if i+1 < len(args) {
							i++
							flags = append(flags, args[i])
						}
					}
				}
			}
		} else {
			positional = append(positional, a)
		}
	}
	if err := f.Parse(flags); err != nil {
		return o, err
	}
	if len(positional) > 2 {
		return o, errors.New("最多接受两个位置参数：学号 密码")
	}
	if len(positional) > 0 {
		if o.User != "" {
			return o, errors.New("学号不能同时用位置参数和 --user 指定")
		}
		o.User = positional[0]
	}
	if len(positional) > 1 {
		if o.Password != "" {
			return o, errors.New("密码不能重复指定")
		}
		o.Password = positional[1]
	}
	if o.User == "" {
		o.User = os.Getenv("NBT_USER")
	}
	if o.Password == "" {
		o.Password = os.Getenv("NBT_PASSWORD")
	}
	if o.Headless && o.Manual {
		return o, errors.New("--manual-login 不能与 --headless 同时使用")
	}
	if o.LoginTimeout <= 0 {
		return o, errors.New("--login-timeout 必须大于零")
	}
	if o.WeekOne != "" {
		if _, err := mondayDate(o.WeekOne); err != nil {
			return o, err
		}
	}
	return o, nil
}

func siteURL(site string) (string, error) {
	switch strings.TrimRight(site, "/") {
	case "vpn", "https://jwxt-443.webvpn.nbt.edu.cn":
		return "https://jwxt-443.webvpn.nbt.edu.cn", nil
	case "direct", "https://jwxt.nbt.edu.cn":
		return "https://jwxt.nbt.edu.cn", nil
	default:
		return "", errors.New("--site 仅支持 vpn、direct 或对应的两个 HTTPS 地址")
	}
}

type terminal struct {
	in          *bufio.Reader
	interactive bool
}

func (t terminal) ask(label string) (string, error) {
	if !t.interactive {
		return "", fmt.Errorf("非交互运行缺少参数：%s", label)
	}
	fmt.Fprint(os.Stderr, label+"：")
	s, err := t.in.ReadString('\n')
	if err != nil {
		return "", errors.New("输入已结束")
	}
	return strings.TrimSpace(s), nil
}

func readOverrides(path string) (map[int]Period, error) {
	if path == "" {
		return nil, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var p map[int]Period
	if err = json.Unmarshal(data, &p); err != nil {
		return nil, errors.New("节次文件应为 {\"1\":{\"start\":\"08:00\",\"end\":\"08:45\"}} 格式")
	}
	for n, v := range p {
		s, e1 := clockTime(v.Start)
		e, e2 := clockTime(v.End)
		if n < 1 || n > 99 || e1 != nil || e2 != nil || s >= e {
			return nil, fmt.Errorf("节次文件中第 %d 节时间无效", n)
		}
		p[n] = Period{s, e}
	}
	return p, nil
}

// Stage the complete file first. Hard-link creation is exclusive, so a file
// that appears during login is never silently overwritten without --force.
func writeOutput(path, contents string, force bool) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".nbt-export-*.tmp")
	if err != nil {
		return err
	}
	temp := f.Name()
	defer os.Remove(temp)
	if _, err = f.WriteString(contents); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if force {
		return os.Rename(temp, path)
	}
	if err = os.Link(temp, path); err != nil {
		return fmt.Errorf("无法创建输出（文件可能已存在；覆盖请加 --force）：%w", err)
	}
	return nil
}

func Run(args []string) error {
	o, err := parseOptions(args, os.Stderr)
	if errors.Is(err, flag.ErrHelp) {
		return nil
	}
	if err != nil {
		return err
	}
	base, err := siteURL(o.Site)
	if err != nil {
		return err
	}
	overrides, err := readOverrides(o.Periods)
	if err != nil {
		return err
	}
	if !o.ListTerms && !o.Force {
		if _, e := os.Lstat(o.Output); e == nil {
			return errors.New("输出文件已存在；请换 --output 路径或使用 --force")
		} else if !errors.Is(e, os.ErrNotExist) {
			return e
		}
	}
	tty := terminal{bufio.NewReader(os.Stdin), term.IsTerminal(int(os.Stdin.Fd()))}
	fmt.Fprintln(os.Stderr, "个人课表导出 · "+base)
	if !o.Manual {
		if o.User == "" {
			o.User, err = tty.ask("学号")
			if err != nil {
				return err
			}
		}
		if o.Password == "" {
			if !tty.interactive {
				return errors.New("缺少密码，请设置 NBT_PASSWORD 或 --password")
			}
			fmt.Fprint(os.Stderr, "密码（输入不显示）：")
			p, e := term.ReadPassword(int(os.Stdin.Fd()))
			fmt.Fprintln(os.Stderr)
			if e != nil {
				return e
			}
			o.Password = string(p)
		}
		if o.User == "" || o.Password == "" {
			return errors.New("学号和密码不能为空")
		}
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	b, cleanup, err := startBrowser(ctx, base, o.Browser, o.Headless)
	if err != nil {
		return err
	}
	defer cleanup()
	fmt.Fprintln(os.Stderr, "正在登录；如出现验证码、二次认证或登录失败，请在浏览器完成操作。")
	page, err := b.login(o.User, o.Password, o.LoginTimeout)
	o.Password = ""
	if err != nil {
		return err
	}
	terms, err := parseTerms(page)
	if err != nil {
		return err
	}
	if o.ListTerms {
		for _, t := range terms {
			suffix := ""
			if t.Current {
				suffix = " [当前]"
			}
			fmt.Printf("%-12s %s%s\n", t.Year+":"+t.Semester, t.Label, suffix)
		}
		return nil
	}
	selected, err := pickTerm(terms, o.Term)
	if tty.interactive && o.Term == "" {
		fmt.Fprintln(os.Stderr, "\n可选学期：")
		defaultIndex := 0
		for i, t := range terms {
			marker := ""
			if t.Current {
				marker = " [当前]"
				defaultIndex = i + 1
			}
			fmt.Fprintf(os.Stderr, "  %d. %s%s\n", i+1, t.Label, marker)
		}
		answer, e := tty.ask(fmt.Sprintf("学期序号（回车选当前 %d）", defaultIndex))
		if e != nil {
			return e
		}
		if answer != "" {
			n, e := strconv.Atoi(answer)
			if e != nil || n < 1 || n > len(terms) {
				return errors.New("学期序号无效")
			}
			selected = terms[n-1]
			err = nil
		}
	}
	if err != nil {
		return err
	}
	fmt.Fprintln(os.Stderr, "读取课表："+selected.Label)
	form := url.Values{"xnm": {selected.Year}, "xqm": {selected.Semester}}
	data, err := b.fetch("/jwglxt/kbcx/xskbcx_cxXsgrkb.html", form)
	if err != nil {
		return err
	}
	timetable, err := parseTimetable(data, selected)
	if err != nil {
		return err
	}
	periodData, periodErr := b.fetch("/jwglxt/kbcx/xskbcx_cxRjc.html", form)
	if periodErr == nil {
		payload, e := decodeJSON(periodData)
		if e == nil {
			for n, p := range parsePeriods(payload) {
				timetable.Periods[n] = p
			}
		}
	} else {
		fmt.Fprintln(os.Stderr, "提示：节次时间接口不可用，将使用课表内时间或 --periods。")
	}
	for n, p := range overrides {
		timetable.Periods[n] = p
	}
	for _, w := range timetable.Warnings {
		fmt.Fprintln(os.Stderr, "注意："+w)
	}
	if len(timetable.Meetings) == 0 {
		return errors.New("该学期没有可导出的定时课程；未生成空文件，请检查学期或实践课程提示")
	}
	if needsMonday(timetable) && o.WeekOne == "" {
		o.WeekOne, err = tty.ask("教学第一周周一（YYYY-MM-DD，以学校校历为准）")
		if err != nil {
			return err
		}
	}
	ics, count, err := makeICS(timetable, o.WeekOne, time.Now())
	if err != nil {
		return err
	}
	if err = writeOutput(o.Output, ics, o.Force); err != nil {
		return err
	}
	fmt.Printf("已导出 %d 次课程 → %s\n", count, o.Output)
	return nil
}
