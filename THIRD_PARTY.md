# 上游来源

课表字段、教务请求路径、学期代码、周次表达式以及 ICS 导出行为参考并移植自：

- 仓库：https://github.com/nbtca/nbtcal
- 参考提交：`a4228efd223f29e2c56c4e8f7b33a398a11127a5`
- 源文件：`src/timetable/client.ts`、`parse.ts`、`ics.ts`、`types.ts`
- 许可：MIT，Copyright (c) 2026 NBTCA；完整原文见 `LICENSE.upstream`。

本工具将相关逻辑移植为 Go，不通过 npm 引用原包；上游更新不会自动同步。

与上游的主要差异：浏览器自动登录与人工验证码接续、终端交互、严格拒绝部分无法识别的周次表达式、缺少定时信息的实践课程以警告显示而不保留原始条目、使用 SHA-256 生成事件 UID（与上游 UID 不兼容）。输出包含明确时间的课程，不导出只有周次而没有星期/节次的实践课程。

浏览器控制使用 `github.com/chromedp/chromedp`；HTML 解析及终端密码输入分别使用 `golang.org/x/net/html`、`golang.org/x/term`。依赖版本和校验值见 `go.mod`、`go.sum`。
