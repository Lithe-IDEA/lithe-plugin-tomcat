package io.github.litheidea.tomcat;

import java.util.Locale;

/**
 * The provider's user-facing text in the interface language Lithe sends as
 * {@code locale} in {@code initialize}: Chinese for {@code zh*}, English
 * otherwise. Lithe shows a failed preparation's message as it is, so no
 * message carries a path, a stack trace or tool output.
 */
final class Messages {
    private final boolean chinese;

    Messages(String locale) {
        chinese = locale != null && locale.toLowerCase(Locale.ROOT).startsWith("zh");
    }

    String noHome() {
        return chinese
                ? "没有设置 Tomcat。请在 Tomcat 设置中选择 Tomcat 安装目录，或设置 CATALINA_HOME 环境变量。"
                : "No Tomcat installation is set. Choose the Tomcat directory in the Tomcat settings, or set CATALINA_HOME.";
    }

    String invalidHome() {
        return chinese
                ? "所选目录不是完整的 Tomcat 安装：缺少 bin/bootstrap.jar、bin/tomcat-juli.jar、lib/catalina.jar 或 conf/web.xml。"
                : "The chosen directory is not a complete Tomcat installation: bin/bootstrap.jar, bin/tomcat-juli.jar, lib/catalina.jar or conf/web.xml is missing.";
    }

    String portBusy(int port) {
        return chinese
                ? "HTTP 端口 " + port + " 已被占用。请停止占用它的程序，或在 Tomcat 设置中换一个端口。"
                : "HTTP port " + port + " is in use. Stop the program using it, or choose another port in the Tomcat settings.";
    }

    String notWebModule(String module) {
        return chinese
                ? module + " 已不是 Web 模块。请重新加载 Tomcat 构建模型。"
                : module + " is no longer a Web module. Reload the Tomcat build model.";
    }

    String building(String module) {
        return chinese ? "正在构建 " + module + "…" : "Building " + module + "…";
    }

    String buildFailed(String tool, int exitCode) {
        return chinese
                ? tool + " 构建失败（退出码 " + exitCode + "），详见运行窗口中的构建输出。"
                : "The " + tool + " build failed (exit code " + exitCode + "). See the build output in the Run window.";
    }

    String noTool(String command) {
        return chinese
                ? "在 PATH 中找不到 " + command + "。请安装它，或在项目里加入对应的 wrapper。"
                : command + " is not on PATH. Install it, or add its wrapper to the project.";
    }

    String noArtifact(String tool) {
        return chinese
                ? tool + " 构建完成，但没有找到唯一的 WAR 产物。请检查模块的打包配置。"
                : "The " + tool + " build finished without exactly one WAR artifact. Check the module's packaging.";
    }

    String unsupportedArguments() {
        return chinese
                ? "Tomcat 运行不接受程序参数。JVM 参数请填在运行配置的“VM 选项”里。"
                : "A Tomcat run takes no program arguments. Put JVM options in the run configuration's VM options.";
    }

    String startingTomcat(String module, String context, int port) {
        return chinese
                ? "Tomcat 将在 http://localhost:" + port + context + " 提供 " + module
                : "Tomcat will serve " + module + " at http://localhost:" + port + context;
    }
}
