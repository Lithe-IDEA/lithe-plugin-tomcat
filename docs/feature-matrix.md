# 功能矩阵

本页记录 Tomcat 插件自己的跨平台功能状态。插件能力不写进 Lithe 主仓库的功能矩阵；改动插件行为的 PR 同时更新本页。

## 状态取值

| 字段 | 取值 |
| --- | --- |
| 实现 | `implemented` 已实现；`partial` 部分实现；`missing` 未实现；`not-applicable` 该平台不适用 |
| 验证 | `verified` 已在真实 Lithe 宿主中按“验证方法”完整验收；`pending` 尚未完整验收；`not-applicable` 无需验收 |

自动化测试的结果写在“最近验证”里，不能代替宿主验收。三个平台的 ZIP 逐字节相同。

## 总览

| ID | 能力 | macOS | Windows | Linux | 最近验证 |
| --- | --- | --- | --- | --- | --- |
| `tomcat-web-modules` | 识别 Maven war、Gradle war、Eclipse Dynamic Web Project 和普通 Web 目录 | implemented / pending | implemented / pending | implemented / pending | 2026-11-18 `scripts/test.mjs` 通过（macOS arm64，Tomcat 9.0.124 与 10.1.62，JDK 17；Tomcat 9.0.124，JDK 8） |
| `tomcat-run` | 构建模块并在私有 CATALINA_BASE 中运行，HTTP 可访问 | implemented / pending | implemented / pending | implemented / pending | 同上；2026-11-18 宿主端到端测试 `TestTomcatPluginRunsAWebModule` 通过（macOS arm64，宿主 1lck/Lithe-IDEA c47ae049，含 #2145，Tomcat 9.0.124 与 10.1.62）：运行配置启动、页面可访问、运行窗口有 Tomcat 日志、Stop 释放端口 |
| `tomcat-in-place` | Eclipse 项目和普通 Web 目录原地运行，挂载编译输出，JSP 修改无需重新部署 | implemented / pending | implemented / pending | implemented / pending | 同上 |
| `tomcat-debug` | 按 `java` 调试类型连接调试，Tomcat 以 `suspend=y` 等待 | implemented / pending | implemented / pending | implemented / pending | `scripts/test.mjs` 验证到 JDWP 握手；2026-11-18 宿主端到端测试 `TestTomcatPluginDebugsAWebModule` 通过（同上，Java Support 4.0.1，JDK 21）：Java 调试器连接，请求停在项目源码断点，Continue 后完成 |
| `tomcat-settings` | Tomcat 安装目录与 HTTP 端口设置，中英文 | implemented / pending | implemented / pending | implemented / pending | 2026-11-18 清单与翻译通过 Core 校验；宿主端到端测试在读取模型后保存设置再运行，新值生效（需要 1lck/Lithe-IDEA#2142 的修复）；宿主界面未验收。提供者错误提示暂时只有英文（1lck/Lithe-IDEA#2144） |

## 证据与验证方法

| ID | 实现 | 自动化测试 | 验证方法（宿主） |
| --- | --- | --- | --- |
| `tomcat-web-modules` | `provider/.../WebModules.java` | `scripts/test.mjs`：“the model lists each Web module and nothing else” | 打开含四类模块的工作区，构建窗口的 Tomcat 实例列出每个 Web 模块，jar 模块不出现 |
| `tomcat-run` | `Preparation.java`、`CatalinaBase.java` | “the Maven module builds…”、“the Gradle war module…” | 从运行配置运行，运行窗口显示构建输出和 Tomcat 启动日志，浏览器打开 `http://localhost:<端口><上下文>/` |
| `tomcat-in-place` | `WebModules.java`（WTP、普通目录）、`CatalinaBase.contextXml` | “the Eclipse project is served in place…”、“a plain Web folder…” | 运行 Eclipse 项目，修改 JSP 后刷新页面看到新内容 |
| `tomcat-debug` | `Preparation.launch`、`plugin.json` 的 `debug.configurationType` | “a debug run suspends Tomcat…” | 装好 Java 插件后调试运行，在 Servlet 或 JSP 编译出的类上设断点并命中 |
| `tomcat-settings` | `plugin.json` 的 `settings`、`l10n/zh-Hans.json` | `scripts/test.mjs`：缺少 Tomcat 时的中文提示 | 中英文界面下打开设置页，修改安装目录和端口后运行使用新值 |

CI（`.github/workflows/ci.yml`）在 Linux 上用 JDK 17 和 JDK 8 运行同一套测试，ready 状态的 PR 还会在 Windows 上用
`mvn.cmd` 和 `gradle.bat` 运行，并在 Linux 上对固定的宿主运行宿主端到端测试（`host` 任务）。
