# Tomcat for Lithe

为 [Lithe](https://github.com/1lck/Lithe-IDEA) 提供 Apache Tomcat 支持：识别工作区里的 Web 模块，构建后在
Tomcat 上运行，并可以用 Java 调试器调试。这是一个**社区插件**，用插件契约 v0 的构建系统接口
（`buildSystems`）和构建提供者（`buildProviders`）实现：一份 `plugin.json` 加一个 Java 8 编写的提供者 jar。
Tomcat 进程由 Lithe 启动、停止和调试，插件只负责准备启动计划。

> English: Apache Tomcat support for Lithe. The plugin finds the Web modules of a workspace (Maven and Gradle
> `war` modules, Eclipse Dynamic Web Projects and plain Web folders), builds them, and prepares a Tomcat launch that
> Lithe starts, stops and debugs like any other service. Each module runs in its own CATALINA_BASE in Lithe's
> workspace state; the Tomcat installation, the workspace and the plugin package are only read.

> **兼容性**：按插件契约 v0（`schemaVersion: 0`、`pluginAPIVersion: 0`）编写，需要支持 `buildSystems 0`、
> `buildProviders 0` 和 `settings 0` 的 Lithe 桌面端。插件不带 Tomcat，运行用户自己安装的 Tomcat。

## 功能

| 功能 | 说明 |
| --- | --- |
| 识别 Web 模块 | Maven：`pom.xml` 的 `<packaging>war</packaging>`。Gradle：`build.gradle(.kts)` 应用了 `war` 插件。Eclipse Dynamic Web Project：`.settings/org.eclipse.wst.common.component` 把某个目录部署到 `/`。普通 Web 目录：`WEB-INF/web.xml` 在 `web`、`WebContent`、`WebRoot`、`webapp` 或 `src/main/webapp` 下 |
| 运行 | 每个 Web 模块在构建窗口和运行配置里有一项“Run on Tomcat”。Maven 模块在反应堆根用 `-pl <模块> -am -DskipTests package` 构建，先构建它依赖的模块；Gradle 模块运行 `war` 任务 |
| 原地运行 | Eclipse 项目和普通 Web 目录不打包，直接以 Web 目录作为应用，编译输出目录（`.classpath` 的 `output`，或 `out/production/<名字>`、`build/classes`、`bin`、`classes`）挂到 `/WEB-INF/classes`，并开启类重载。修改 JSP、HTML 后刷新即可看到，不需要重新部署 |
| 调试 | 运行配置选“调试”时，Tomcat 以 `suspend=y` 等待调试器，Lithe 用已安装的 Java 调试器（调试类型 `java`）连接到它自己分配的端口 |
| 访问地址 | `http://localhost:<端口><上下文路径>/`。上下文路径：Eclipse 项目用组件文件的 `context-root`，Maven 模块用 `artifactId`，其他用目录名 |
| 同步 | `pom.xml`、`build.gradle(.kts)`、`.project`、`web.xml` 变化后重新读取模块 |

## 设置

设置 → 插件 → Tomcat →「设置」，也显示在 Tomcat 构建系统的设置页：

| 设置 | 说明 |
| --- | --- |
| Tomcat 安装目录 | 运行用的 Tomcat（CATALINA_HOME）。留空时使用环境变量 `CATALINA_HOME`。必须包含 `bin/bootstrap.jar`、`bin/tomcat-juli.jar`、`lib/catalina.jar` 和 `conf/web.xml` |
| HTTP 端口 | Tomcat 在 `127.0.0.1` 上监听的端口，默认 8080，可按项目覆盖。运行前会检查端口是否被占用 |

运行配置里的“VM 选项”和环境变量会传给 Tomcat 的 JVM。Tomcat 运行不接受程序参数，填写时运行会报错，而不是被悄悄忽略。

## 工作方式

1. Lithe 发现工作区里有 `pom.xml`、`build.gradle(.kts)`、`.project` 或 `web.xml` 时，建立 Tomcat 构建实例。
   用户第一次打开构建窗口或运行时，Lithe 用项目 JDK 启动 `lib/lithe-tomcat-provider.jar`。
2. 提供者回答 `lithe/build/model`：有界遍历实例目录（最多 8 层、5000 个目录，跳过 `target`、`build`、
   `out`、`bin`、`node_modules`、`src` 和以点开头的目录），每个 Web 模块是一个项目和一个带
   `web-application` 标签的目标。没有 Web 模块的 Maven 或 Gradle 项目，模型为空。
3. 运行时提供者回答 `lithe/build/prepareRun`：
   - 重新检测模块，模块已经不是 Web 模块时报错，不使用过期的模型；
   - 检查 Tomcat 安装目录和 HTTP 端口；
   - 用项目的 Maven Wrapper、`MAVEN_HOME` 或 `PATH` 上的 `mvn`（Gradle 同理：`gradlew`、`GRADLE_HOME`、`gradle`）构建，输出显示在运行窗口；
     没有设置 `JAVA_HOME` 时，构建使用 Lithe 启动提供者的同一个 JDK；
   - 在 Lithe 的工作区状态目录 `bases/<哈希>/` 写出这个模块专用的 CATALINA_BASE：`server.xml`（只有一个绑定
     `127.0.0.1` 的 HTTP 连接器，关闭端口为 `-1`）、只输出到控制台的 `logging.properties`、从安装目录复制的其他
     `conf/` 文件，以及指向模块的 `conf/Catalina/localhost/<上下文>.xml`；
   - 返回启动计划：在项目 JDK 上运行 `org.apache.catalina.startup.Bootstrap start`，系统属性与 `catalina.sh run`
     相同，`JDK_JAVA_OPTIONS` 带上 `catalina.sh` 在 Java 9+ 使用的 `--add-opens`（Java 8 会忽略这个变量）。
4. Lithe 启动、停止 Tomcat，并把标准输出和标准错误显示在运行窗口。

插件只读 Tomcat 安装目录、工作区和插件包。每次运行会重写 CATALINA_BASE 的 `conf/`、清空 `webapps/` 和
`temp/`；`work/`（编译过的 JSP）保留，下次启动更快。Gradle 构建结束后 Gradle daemon 按 Gradle 自己的规则留在后台，
和在终端运行 `gradle` 一样。

## 诊断日志

提供者用 Java SDK 与 Lithe 协商日志协议，日志进入 Lithe 日志窗口的该插件通道：

| 操作 | 成功 | 失败（ERROR） | 降级（WARN） |
| --- | --- | --- | --- |
| `lithe/build/model` 读取模块 | 不记录（被监视的文件一改就重新读取，属于高频读取） | SDK 记录真实失败，类别 `tomcat.invalidRoot` 或 `internal_error` | `buildFileUnreadable`（构建文件存在但读不了或无法解析，按次数记一次）；`webModuleScanTruncated`（目录数超过上限，模型不完整） |
| `lithe/build/prepareRun` 准备运行 | INFO（SDK 记录一次） | `tomcat.run.noHome`、`invalidHome`、`portInUse`、`notWebModule`、`buildFailed`、`noTool`、`noArtifact`、`unsupportedArguments`、`invalidProject`、`invalidDebug` | `buildFileUnreadable` |

取消运行准备不记录失败。日志只带类别和计数，不含路径、命令输出或用户输入；返回给用户的错误提示也不含路径。
Maven、Gradle 的构建输出和 Tomcat 自己的日志只出现在运行窗口，不写日志文件。

## 安装

Lithe Store 收录后，在 设置 → 插件 → 插件市场 搜索 Tomcat。也可以从
[Releases](https://github.com/Lithe-IDEA/lithe-plugin-tomcat/releases) 下载 `tomcat-<版本>-<平台>.zip`，在
设置 → 插件 中点击 从文件安装…。三个平台的 ZIP 内容相同。调试需要另装提供 `java` 调试类型的 Java 插件。

## 开发

需要 Node.js 22、JDK 17 或更新版本（编译用 `javac --release 8`）、Maven 和 `zip`。

```sh
node scripts/package.mjs          # 校验 SDK 哈希，编译提供者，生成 dist/tomcat-<版本>-{darwin,linux,windows}.zip
node scripts/test/fetch-tomcat.mjs # 下载 test/tomcat-versions.json 固定的 Tomcat，按 SHA-512 校验后解压
node scripts/test.mjs             # 用真实 Tomcat 运行端到端测试
```

`scripts/test.mjs` 对每个 Tomcat（默认 `.artifacts/tomcat/apache-tomcat-*`，或 `LITHE_TEST_TOMCAT_HOMES`）
以 Lithe 的方式启动打包后的提供者，读取 `test/fixtures/workspace/` 中四个实例的模型，准备并启动每个模块，通过
HTTP 读取页面；同时检查调试启动（JDWP 握手）、端口占用、程序参数和过期目标的报错、修改 JSP 后无需重新部署、
日志记录、以及插件包和 Tomcat 安装目录在测试前后完全一致。Gradle 用 `GRADLE_HOME`、`PATH` 或
`.artifacts/gradle/gradle-*`，测试使用独立的 `GRADLE_USER_HOME` 并在结束时停止它启动的 daemon。

### 构建资源

| 资源 | 来源与校验 | 位置 | 能否在工作树之间复制 |
| --- | --- | --- | --- |
| Java 插件 SDK | Core 仓库 `shared/plugins/sdk/java`，`sdk.lock.json` 记录 commit 和每个文件的 SHA-256，打包前校验 | `vendor/`（入库） | 随仓库 |
| 提供者 jar | 打包时用 `javac --release 8` 编译 | `provider/target/`、`dist/` | 可以，但每次打包都会重建 |
| Tomcat（仅测试） | `test/tomcat-versions.json` 固定版本、URL 和 SHA-512 | `.artifacts/tomcat/` | 可以，按版本目录复制；不进插件包 |
| Gradle（仅本地测试） | Gradle 官方发行包，按 `.sha256` 校验 | `.artifacts/gradle/` | 可以 |
| 测试工作目录 | 每次测试重建 | `.artifacts/test-work/` | 不要复制，包含运行时状态 |

## 功能矩阵

见 [docs/feature-matrix.md](docs/feature-matrix.md)。

## 许可

Apache License 2.0，见 [LICENSE](LICENSE) 和 [NOTICE](NOTICE)。
