# NBT 个人课表导出

基于 [nbtca/nbtcal](https://github.com/nbtca/nbtcal) 的 JWXT 协议与课表解析规则实现的 Go 命令行工具。支持交互输入、命令行账号密码、学期选择和 `.ics` 导出，不需要安装 Node.js 或手动复制 Cookie。

支持两个入口：

| 选项 | 地址 |
| --- | --- |
| `--site vpn`（默认） | https://jwxt-443.webvpn.nbt.edu.cn |
| `--site direct` | https://jwxt.nbt.edu.cn |

也可以将完整地址传给 `--site`。直连需要你的网络能访问教务系统；校外通常使用 WebVPN。

## 快速开始

需要 **Go 1.24+** 和本机 **Chrome / Chromium**。首次运行 Go 会下载依赖。

```sh
go run main.go
```

按提示输入学号、密码（不回显）。程序打开独立浏览器窗口，自动填写并提交学校登录表单。完成登录后，在终端选择学期，按需输入教学第一周周一，生成 `个人课表.ics`。

也支持你希望的位置参数形式（选项可以在学号密码之后）：

```sh
go run main.go '学号' '密码' --site vpn
go run main.go '学号' '密码' --site direct
```

命令行密码可能进入 shell 历史；日常建议只提供学号，交互输入密码：

```sh
go run main.go --user '学号' --site direct
```

程序沿用学校页面自身的密码加密、CAS/WebVPN 跳转和 Cookie。**遇到滑块、图片验证码、二次认证或账号密码错误时，在弹出的浏览器中处理。** 脚本不会自动识别或绕过验证码，不会不断重试错误密码。网页结构不匹配时也可以在同一窗口手动登录，随后程序继续读取课表。

## 常用选项

```sh
# 明确指定学期、第一周周一、输出位置
# 日期仅为示例，必须替换成对应学期的实际教学第一周周一。
go run main.go --user '学号' --site vpn \
  --term 2026-1 --week-one 2026-09-07 --output autumn.ics

# 查看教务系统提供的学期代码
go run main.go --list-terms

# 全程在浏览器手动登录，不在终端输入账号密码
go run main.go --manual-login

# 指定 Chrome / Chromium 路径
go run main.go --browser '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome'

# 允许覆盖已有日历
go run main.go --output 个人课表.ics --force

# 查看完整帮助
go run main.go --help
```

学期支持 `2026-1`（2026–2027 学年第一学期）、`2026-2`、`2026-3`，以及教务原始代码 `2026:3`、`2026:12`、`2026:16`。可选范围以 `--list-terms` 返回为准。非交互运行未指定学期时使用教务页面标注的当前学期。

可以设置 `NBT_USER`、`NBT_PASSWORD` 环境变量。使用 `--headless` 隐藏浏览器前，应确认本次登录不需要验证码或二次认证；需要验证时请去掉这个选项重新运行。默认等待登录 5 分钟，可用 `--login-timeout 10m` 调整。

## 日期和上课时间

优先使用教务接口提供的日期与节次时间。没有完整教学日期表时，工具会要求输入第一周周一；非交互运行用 `--week-one YYYY-MM-DD`。不内置或猜测开学日期。

如果报“缺少第 N 节上课时间”，创建 `periods.json`，填入**学校实际作息时间**，覆盖所有所需节次。例如下列内容只演示格式，不代表学校作息：

```json
{
  "1": { "start": "08:00", "end": "08:45" },
  "2": { "start": "08:50", "end": "09:35" }
}
```

```sh
go run main.go --periods periods.json
```

导出的日历使用 `Asia/Shanghai` 时区，按周展开单/双周课程。缺少星期、节次等信息的实践课程会提示并跳过，不会凭空安排时间。没有任何可导出的定时课程时不会生成空文件。节假日调课以教务返回的数据为准；只有周次规则时，无法推断未体现在接口中的临时调课。

`.ics` 是导出快照。课表更新后需重新导出，导入时建议使用独立的课程日历；不同日历应用重复导入的合并行为可能不同。

## 构建和验证

```sh
go test ./...
go build -o nbt-export .
./nbt-export --help

# 可选：启动本机 Chrome，对本地模拟教务服务执行集成测试
NBT_BROWSER_TEST=1 go test ./internal/app -run TestBrowserIntegration -v
```

测试覆盖学期核对、单双周、权威日期映射、重复课程、缺失节次、中文转义与 75 字节折行、参数解析、文件覆盖保护。浏览器集成测试验证同源 Cookie、POST 请求和 ICS 导出，仅访问本机模拟服务，不向学校提交测试账号。

## 数据与来源

密码只用于当前浏览器登录，不写入配置或日志。浏览器使用临时独立配置，不读取日常 Chrome 登录状态，正常退出后删除临时配置。课表原始响应不保存到磁盘；生成的 ICS 包含课程、教师和地点，请按个人课表管理。

上游解析规则的移植说明见 [THIRD_PARTY.md](THIRD_PARTY.md)，保留的上游 MIT 许可见 [LICENSE.upstream](LICENSE.upstream)。
