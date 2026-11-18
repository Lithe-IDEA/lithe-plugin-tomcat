package io.github.litheidea.tomcat;

import io.github.litheidea.plugin.sdk.CallContext;
import io.github.litheidea.plugin.sdk.Json;
import io.github.litheidea.plugin.sdk.PluginError;
import io.github.litheidea.plugin.sdk.Processes;
import java.io.File;
import java.io.IOException;
import java.net.InetAddress;
import java.net.InetSocketAddress;
import java.net.ServerSocket;
import java.net.Socket;
import java.nio.file.DirectoryStream;
import java.nio.file.Files;
import java.nio.file.Path;
import java.nio.file.Paths;
import java.util.ArrayList;
import java.util.Arrays;
import java.util.Collections;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Locale;
import java.util.Map;
import org.w3c.dom.Document;
import org.w3c.dom.Node;

/**
 * Answers {@code lithe/build/prepareRun} for a {@code web-application}
 * target: builds the module when Maven or Gradle owns it, writes the
 * module's private CATALINA_BASE ({@link CatalinaBase}) and returns the
 * launch of {@code org.apache.catalina.startup.Bootstrap start} on the
 * project's JDK, as {@code catalina.sh run} would start it. Lithe starts,
 * stops and debugs that process.
 *
 * <p>An Eclipse Dynamic Web Project or a plain Web folder is served where it
 * is, with its compiled classes mounted from the output folder the Java
 * plugin builds into, so an edit needs no deployment step.
 */
final class Preparation {
    static final String JAVA_TOOL = "java";
    static final String BOOTSTRAP = "org.apache.catalina.startup.Bootstrap";
    /** The modules {@code catalina.sh} opens to Tomcat on Java 9 and later; Java 8 ignores the variable. */
    static final List<String> ADD_OPENS = Collections.unmodifiableList(Arrays.asList(
            "java.base/java.lang", "java.base/java.lang.invoke", "java.base/java.lang.reflect", "java.base/java.io",
            "java.base/java.util", "java.base/java.util.concurrent", "java.rmi/sun.rmi.transport"));
    private static final int OUTPUT_CHUNK = 32 * 1024;

    private final Path workspace;
    private final Path state;
    private final Settings settings;
    private final Messages messages;

    Preparation(Path workspace, Path state, Settings settings, Messages messages) {
        this.workspace = workspace;
        this.state = state;
        this.settings = settings;
        this.messages = messages;
    }

    Map<String, Object> prepare(Map<String, Object> params, CallContext context) throws Exception {
        Request request = Request.of(params);
        if (!request.arguments.isEmpty() || !request.userArguments.isEmpty()) {
            throw new PluginError("tomcat.run.unsupportedArguments", messages.unsupportedArguments());
        }
        Path instance = workspace.resolve(request.root).normalize();
        String projectPath = relative(request.root, request.projectPath);
        Path directory = instance.resolve(projectPath).normalize();
        if (!directory.startsWith(instance)) {
            throw new PluginError("tomcat.run.invalidProject", "The project is outside the build root.");
        }
        WebModules.Unreadable unreadable = new WebModules.Unreadable();
        WebModules.Module module = WebModules.detect(instance, directory, unreadable);
        unreadable.report(context, "lithe/build/prepareRun");
        if (module == null || (request.kind != null && !request.kind.equals(module.kind.id))) {
            throw new PluginError("tomcat.run.notWebModule", messages.notWebModule(request.targetName));
        }
        Path home = home();
        int port = settings.httpPort;
        if (inUse(port)) {
            throw new PluginError("tomcat.run.portInUse", messages.portBusy(port));
        }
        Map<String, String> environment = environment(request.environment);
        Path docBase;
        Path classes = null;
        switch (module.kind) {
            case MAVEN:
                docBase = maven(context, instance, directory, module, request, environment);
                break;
            case GRADLE:
                docBase = gradle(context, instance, directory, module, request, environment);
                break;
            default:
                docBase = instance.resolve(module.webRoot).normalize();
                if (module.classes != null) {
                    classes = instance.resolve(module.classes).normalize();
                }
                break;
        }
        CatalinaBase base = CatalinaBase.write(state, home, request.root + "\n" + projectPath, port, module.contextPath,
                docBase, classes);
        context.progress(messages.startingTomcat(module.name, module.contextPath.isEmpty() ? "/" : module.contextPath, port),
                null, null);
        return launch(request, home, base.directory);
    }

    /** The launch plan; {@code catalina.sh run} with the same system properties. */
    Map<String, Object> launch(Request request, Path home, Path base) {
        List<String> arguments = new ArrayList<>(request.runtimeArguments);
        if (request.debugPort > 0) {
            arguments.add("-agentlib:jdwp=transport=dt_socket,server=y,suspend=y,address="
                    + request.debugHost + ":" + request.debugPort);
        }
        arguments.add("-Dcatalina.home=" + home);
        arguments.add("-Dcatalina.base=" + base);
        arguments.add("-Djava.io.tmpdir=" + base.resolve("temp"));
        Path logging = base.resolve("conf").resolve("logging.properties");
        if (Files.isRegularFile(logging)) {
            arguments.add("-Djava.util.logging.config.file=" + logging);
        }
        arguments.add("-Djava.util.logging.manager=org.apache.juli.ClassLoaderLogManager");
        arguments.add("-Djdk.tls.ephemeralDHKeySize=2048");
        arguments.add("-Djava.protocol.handler.pkgs=org.apache.catalina.webresources");
        arguments.add("-classpath");
        arguments.add(home.resolve("bin").resolve("bootstrap.jar") + File.pathSeparator
                + home.resolve("bin").resolve("tomcat-juli.jar"));
        arguments.add(BOOTSTRAP);
        arguments.add("start");
        Map<String, String> environment = new LinkedHashMap<>(request.launchEnvironment);
        StringBuilder opens = new StringBuilder();
        for (String module : ADD_OPENS) {
            opens.append(opens.length() == 0 ? "" : " ").append("--add-opens=").append(module).append("=ALL-UNNAMED");
        }
        String existing = environment.get("JDK_JAVA_OPTIONS");
        environment.put("JDK_JAVA_OPTIONS", existing == null || existing.trim().isEmpty()
                ? opens.toString() : existing + " " + opens);
        Map<String, Object> executable = new LinkedHashMap<>();
        executable.put("kind", "toolchain");
        executable.put("value", JAVA_TOOL);
        Map<String, Object> plan = new LinkedHashMap<>();
        plan.put("executable", executable);
        plan.put("arguments", arguments);
        plan.put("workingDirectory", request.workingDirectory != null ? request.workingDirectory : request.projectPath);
        plan.put("environment", environment);
        return plan;
    }

    /** The Tomcat installation: the setting, otherwise {@code CATALINA_HOME}. */
    Path home() {
        String chosen = settings.tomcatHome.trim();
        if (chosen.isEmpty()) {
            String variable = System.getenv("CATALINA_HOME");
            chosen = variable == null ? "" : variable.trim();
        }
        if (chosen.isEmpty()) {
            throw new PluginError("tomcat.run.noHome", messages.noHome());
        }
        Path home;
        try {
            home = Paths.get(chosen).toAbsolutePath().normalize();
        } catch (java.nio.file.InvalidPathException invalid) {
            throw new PluginError("tomcat.run.invalidHome", messages.invalidHome());
        }
        for (String required : new String[] {"bin/bootstrap.jar", "bin/tomcat-juli.jar", "lib/catalina.jar", "conf/web.xml"}) {
            if (!Files.isRegularFile(home.resolve(required))) {
                throw new PluginError("tomcat.run.invalidHome", messages.invalidHome());
            }
        }
        return home;
    }

    /** True when something accepts connections on, or holds, the loopback port. */
    static boolean inUse(int port) {
        InetAddress loopback = InetAddress.getLoopbackAddress();
        try (Socket socket = new Socket()) {
            socket.connect(new InetSocketAddress(loopback, port), 300);
            return true;
        } catch (IOException free) {
            // Nothing answers; a bound but idle socket is caught below.
        }
        // The platform's default address reuse, as Tomcat binds its connector: a
        // port a just-stopped Tomcat left in TIME_WAIT is free for the next run.
        try (ServerSocket socket = new ServerSocket()) {
            socket.bind(new InetSocketAddress(loopback, port));
            return false;
        } catch (IOException taken) {
            return true;
        }
    }

    /**
     * Builds a Maven {@code war} module with {@code package -DskipTests} and
     * returns the exploded application Maven writes next to the WAR. A
     * module of a reactor builds from the reactor root with {@code -pl -am},
     * so the modules it depends on are built first.
     */
    private Path maven(CallContext context, Path instance, Path directory, WebModules.Module module, Request request,
            Map<String, String> environment) throws Exception {
        Path root = mavenRoot(instance, directory);
        boolean windows = File.separatorChar == '\\';
        List<String> command = new ArrayList<>();
        command.add(executable(mavenCommand(root, environment, windows), environment, windows));
        command.add("-B");
        command.add("-ntp");
        command.addAll(request.optionArguments);
        if (!root.equals(directory)) {
            command.add("-pl");
            command.add("./" + WebModules.relative(root, directory));
            command.add("-am");
        }
        command.add("-DskipTests");
        command.add("package");
        build(context, command, root, environment, module, "Maven");
        Path war = singleWar(directory.resolve("target"), "Maven");
        String name = war.getFileName().toString();
        Path exploded = war.resolveSibling(name.substring(0, name.length() - ".war".length()));
        return Files.isDirectory(exploded.resolve("WEB-INF")) ? exploded : war;
    }

    /** Builds a Gradle {@code war} module with its {@code war} task and returns the WAR. */
    private Path gradle(CallContext context, Path instance, Path directory, WebModules.Module module, Request request,
            Map<String, String> environment) throws Exception {
        Path root = gradleRoot(instance, directory);
        boolean windows = File.separatorChar == '\\';
        List<String> command = new ArrayList<>();
        command.add(executable(gradleCommand(root, environment, windows), environment, windows));
        command.add("--console=plain");
        command.addAll(request.optionArguments);
        command.add("-p");
        command.add(directory.toString());
        command.add("war");
        build(context, command, root, environment, module, "Gradle");
        return singleWar(directory.resolve("build").resolve("libs"), "Gradle");
    }

    private void build(CallContext context, List<String> command, Path directory, Map<String, String> environment,
            WebModules.Module module, String tool) throws Exception {
        context.progress(messages.building(module.name), null, null);
        Processes.Result result = context.run(command, new Processes.Options()
                .directory(directory)
                .environment(environment)
                .cancellation(context.cancellation())
                .maxOutputBytes(64 * 1024)
                .onStdout(text -> stream(context, text))
                .onStderr(text -> stream(context, text)));
        if (result.exitCode() != 0) {
            throw new PluginError("tomcat.run.buildFailed", messages.buildFailed(tool, result.exitCode()));
        }
    }

    /** The one WAR in {@code directory}, leaving out Gradle's {@code -plain} archive. */
    private Path singleWar(Path directory, String tool) throws IOException {
        List<Path> wars = new ArrayList<>();
        if (Files.isDirectory(directory)) {
            try (DirectoryStream<Path> files = Files.newDirectoryStream(directory, "*.war")) {
                for (Path file : files) {
                    if (Files.isRegularFile(file) && !file.getFileName().toString().endsWith("-plain.war")) {
                        wars.add(file);
                    }
                }
            }
        }
        if (wars.size() != 1) {
            throw new PluginError("tomcat.run.noArtifact", messages.noArtifact(tool));
        }
        return wars.get(0);
    }

    /**
     * The outermost directory, inside the instance, whose {@code pom.xml}
     * lists the next directory down as a module, starting at the module.
     */
    static Path mavenRoot(Path instance, Path directory) {
        Path root = directory;
        while (!root.equals(instance) && root.getParent() != null && root.getParent().startsWith(instance)) {
            Path parent = root.getParent();
            Path pom = parent.resolve("pom.xml");
            if (!Files.isRegularFile(pom) || !listsModule(pom, parent, root)) {
                break;
            }
            root = parent;
        }
        return root;
    }

    private static boolean listsModule(Path pom, Path parent, Path child) {
        Document document = WebModules.xml(pom);
        if (document == null) {
            return false;
        }
        org.w3c.dom.NodeList modules = document.getElementsByTagNameNS("*", "module");
        for (int index = 0; index < modules.getLength(); index++) {
            Node node = modules.item(index);
            String text = node.getTextContent().trim();
            if (!text.isEmpty() && parent.resolve(text).normalize().equals(child)) {
                return true;
            }
        }
        return false;
    }

    /** The nearest directory, from the module up to the instance root, holding a Gradle settings file. */
    static Path gradleRoot(Path instance, Path directory) {
        for (Path candidate = directory; candidate != null && candidate.startsWith(instance); candidate = candidate.getParent()) {
            if (Files.isRegularFile(candidate.resolve("settings.gradle")) || Files.isRegularFile(candidate.resolve("settings.gradle.kts"))) {
                return candidate;
            }
        }
        return directory;
    }

    /** The project's Maven wrapper, otherwise {@code MAVEN_HOME}, otherwise {@code mvn}. */
    static String mavenCommand(Path root, Map<String, String> environment, boolean windows) {
        Path wrapper = root.resolve(windows ? "mvnw.cmd" : "mvnw");
        if (Files.isRegularFile(wrapper) && Files.isRegularFile(root.resolve(".mvn/wrapper/maven-wrapper.properties"))) {
            return wrapper.toString();
        }
        String home = variable(environment, "MAVEN_HOME", windows);
        if (home != null && !home.isEmpty()) {
            Path candidate = Paths.get(home, "bin", windows ? "mvn.cmd" : "mvn");
            if (Files.isRegularFile(candidate)) {
                return candidate.toString();
            }
        }
        return "mvn";
    }

    /** The project's Gradle wrapper, otherwise {@code GRADLE_HOME}, otherwise {@code gradle}. */
    static String gradleCommand(Path root, Map<String, String> environment, boolean windows) {
        Path wrapper = root.resolve(windows ? "gradlew.bat" : "gradlew");
        if (Files.isRegularFile(wrapper) && Files.isRegularFile(root.resolve("gradle/wrapper/gradle-wrapper.properties"))) {
            return wrapper.toString();
        }
        String home = variable(environment, "GRADLE_HOME", windows);
        if (home != null && !home.isEmpty()) {
            Path candidate = Paths.get(home, "bin", windows ? "gradle.bat" : "gradle");
            if (Files.isRegularFile(candidate)) {
                return candidate.toString();
            }
        }
        return "gradle";
    }

    /**
     * The path to start for {@code command}: a bare name is looked up on the
     * {@code PATH} of {@code environment}, trying each {@code PATHEXT}
     * extension on Windows, where Java would otherwise find only
     * {@code .exe} files.
     */
    String executable(String command, Map<String, String> environment, boolean windows) {
        if (command.indexOf('/') >= 0 || (windows && (command.indexOf('\\') >= 0 || command.indexOf(':') >= 0))) {
            return command;
        }
        List<String> names = new ArrayList<>();
        String extensions = variable(environment, "PATHEXT", windows);
        if (windows && command.indexOf('.') < 0) {
            for (String extension : (extensions == null || extensions.isEmpty() ? ".COM;.EXE;.BAT;.CMD" : extensions).split(";")) {
                if (!extension.isEmpty()) {
                    names.add(command + extension.toLowerCase(Locale.ROOT));
                }
            }
        } else {
            names.add(command);
        }
        String path = variable(environment, "PATH", windows);
        if (path != null) {
            for (String folder : path.split(File.pathSeparator)) {
                if (folder.isEmpty()) {
                    continue;
                }
                for (String name : names) {
                    Path candidate;
                    try {
                        candidate = Paths.get(folder.replace("\"", ""), name);
                    } catch (java.nio.file.InvalidPathException ignored) {
                        break;
                    }
                    if (Files.isRegularFile(candidate) && (windows || Files.isExecutable(candidate))) {
                        return candidate.toString();
                    }
                }
            }
        }
        throw new PluginError("tomcat.run.noTool", messages.noTool(command));
    }

    private static String variable(Map<String, String> environment, String name, boolean windows) {
        if (!windows) {
            return environment.get(name);
        }
        for (Map.Entry<String, String> entry : environment.entrySet()) {
            if (entry.getKey().equalsIgnoreCase(name)) {
                return entry.getValue();
            }
        }
        return null;
    }

    /**
     * The build's environment: the provider's own with the build options'
     * variables on top, and {@code JAVA_HOME} set to the JDK Lithe started
     * the provider on when nothing sets it, so the build uses the project JDK.
     */
    static Map<String, String> environment(Map<String, String> overrides) {
        Map<String, String> environment = new LinkedHashMap<>(System.getenv());
        environment.putAll(overrides);
        if (variable(environment, "JAVA_HOME", File.separatorChar == '\\') == null) {
            Path java = Paths.get(System.getProperty("java.home"));
            // A Java 8 runtime reports its jre folder; the JDK is its parent.
            if (java.getFileName() != null && "jre".equals(java.getFileName().toString())
                    && Files.isDirectory(java.getParent().resolve("bin"))) {
                java = java.getParent();
            }
            environment.put("JAVA_HOME", java.toString());
        }
        return environment;
    }

    private static void stream(CallContext context, String text) {
        for (int start = 0; start < text.length(); start += OUTPUT_CHUNK) {
            context.progress(null, null, text.substring(start, Math.min(text.length(), start + OUTPUT_CHUNK)));
        }
    }

    /** {@code path} relative to {@code root}, both workspace-relative with forward slashes. */
    static String relative(String root, String path) {
        if (".".equals(root) || root.isEmpty()) {
            return path.isEmpty() ? "." : path;
        }
        if (path.equals(root)) {
            return ".";
        }
        if (path.startsWith(root + "/")) {
            return path.substring(root.length() + 1);
        }
        throw new PluginError("tomcat.run.invalidProject", "The project is outside the build root.");
    }

    static List<String> strings(Object value) {
        List<String> result = new ArrayList<>();
        if (value instanceof List) {
            for (Object item : (List<?>) value) {
                result.add(String.valueOf(item));
            }
        }
        return result;
    }

    private static Map<String, String> stringMap(Object value) {
        Map<String, String> result = new LinkedHashMap<>();
        if (value instanceof Map) {
            for (Map.Entry<?, ?> entry : ((Map<?, ?>) value).entrySet()) {
                result.put(String.valueOf(entry.getKey()), String.valueOf(entry.getValue()));
            }
        }
        return result;
    }

    private static Map<String, Object> object(Object value) {
        return value instanceof Map ? Json.object(value) : Collections.<String, Object>emptyMap();
    }

    private static String text(Object value) {
        if (value == null) {
            return null;
        }
        String text = value.toString().trim();
        return text.isEmpty() ? null : text;
    }

    /** The fields of one prepareRun request this provider uses. */
    static final class Request {
        String root = ".";
        String projectPath = ".";
        String targetName = "";
        String kind;
        List<String> optionArguments = new ArrayList<>();
        Map<String, String> environment = new LinkedHashMap<>();
        List<String> runtimeArguments = new ArrayList<>();
        List<String> arguments = new ArrayList<>();
        List<String> userArguments = new ArrayList<>();
        Map<String, String> launchEnvironment = new LinkedHashMap<>();
        String workingDirectory;
        String debugHost;
        int debugPort;

        static Request of(Map<String, Object> params) {
            Request request = new Request();
            String root = text(params.get("root"));
            request.root = root == null ? "." : root;
            String projectPath = text(object(params.get("project")).get("path"));
            request.projectPath = projectPath == null ? request.root : projectPath;
            Map<String, Object> target = object(params.get("target"));
            String name = text(target.get("name"));
            request.targetName = name == null ? "" : name;
            request.kind = text(object(target.get("properties")).get("kind"));
            request.optionArguments = strings(params.get("optionArguments"));
            request.environment = stringMap(params.get("environment"));
            request.userArguments = strings(params.get("userArguments"));
            Map<String, Object> options = object(params.get("launchOptions"));
            request.runtimeArguments = strings(options.get("runtimeArguments"));
            request.arguments = strings(options.get("arguments"));
            request.launchEnvironment = stringMap(options.get("environment"));
            request.workingDirectory = text(options.get("workingDirectory"));
            if (options.get("debug") instanceof Map) {
                Map<String, Object> endpoint = object(Json.object(options.get("debug")).get("target"));
                request.debugHost = text(endpoint.get("host"));
                Object port = endpoint.get("port");
                request.debugPort = port instanceof Number ? ((Number) port).intValue() : 0;
                if (request.debugHost == null || request.debugPort < 1 || request.debugPort > 65535) {
                    throw new PluginError("tomcat.run.invalidDebug", "The debug target is not a local connection.");
                }
            }
            return request;
        }
    }
}
