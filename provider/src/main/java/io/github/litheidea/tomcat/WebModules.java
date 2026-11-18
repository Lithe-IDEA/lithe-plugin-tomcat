package io.github.litheidea.tomcat;

import io.github.litheidea.plugin.sdk.CallContext;
import io.github.litheidea.plugin.sdk.PluginError;
import java.io.IOException;
import java.io.InputStream;
import java.nio.charset.StandardCharsets;
import java.nio.file.DirectoryStream;
import java.nio.file.Files;
import java.nio.file.Path;
import java.util.ArrayDeque;
import java.util.ArrayList;
import java.util.Arrays;
import java.util.Collections;
import java.util.Deque;
import java.util.HashSet;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Map;
import java.util.Set;
import java.util.regex.Pattern;
import javax.xml.parsers.DocumentBuilder;
import javax.xml.parsers.DocumentBuilderFactory;
import org.w3c.dom.Document;
import org.w3c.dom.Element;
import org.w3c.dom.Node;
import org.w3c.dom.NodeList;

/**
 * Finds the Web modules of one Tomcat build instance and answers
 * {@code lithe/build/model} with one project and one
 * {@code web-application} target for each.
 *
 * <p>A directory is a Web module when, checked in this order:
 * <ol>
 *   <li>its {@code pom.xml} has {@code <packaging>war</packaging>} (Maven);</li>
 *   <li>its {@code build.gradle} or {@code build.gradle.kts} applies the
 *       {@code war} plugin (Gradle);</li>
 *   <li>its Eclipse WTP component file deploys a folder to {@code /}
 *       (Eclipse Dynamic Web Project);</li>
 *   <li>one of the usual Web roots holds {@code WEB-INF/web.xml}.</li>
 * </ol>
 * A directory with a Maven or Gradle build file is only ever a Maven or
 * Gradle module, so an Eclipse or IntelliJ project generated from it is not
 * listed twice. The scan reads no file twice per request and keeps nothing
 * between requests: Lithe asks again when a watched file changes.
 */
final class WebModules {
    static final String TAG = "web-application";
    /** Directory levels below the instance root the scan enters. */
    static final int MAX_DEPTH = 8;
    /** Directories one scan visits before it reports the model as partial. */
    static final int MAX_DIRECTORIES = 5_000;
    private static final int MAX_BUILD_FILE = 1 << 20;
    private static final Set<String> SKIPPED = new HashSet<>(Arrays.asList(
            "target", "build", "out", "bin", "node_modules", "src", "WEB-INF", "META-INF"));
    static final List<String> WEB_ROOTS = Collections.unmodifiableList(Arrays.asList(
            "src/main/webapp", "WebContent", "WebRoot", "webapp", "web"));
    private static final Pattern GRADLE_WAR = Pattern.compile(
            "(?m)^\\s*(?:war\\s*$|id\\s*\\(?\\s*[\"']war[\"']|apply\\s*\\(?\\s*plugin\\s*[:=]\\s*[\"']war[\"'])");
    private static final String WTP_COMPONENT = ".settings/org.eclipse.wst.common.component";

    /** How a module is built and where its files come from. */
    enum Kind {
        MAVEN("maven"), GRADLE("gradle"), WTP("eclipse"), PLAIN("plain");

        final String id;

        Kind(String id) {
            this.id = id;
        }
    }

    /** One Web module; paths are relative to the instance root, with {@code /}. */
    static final class Module {
        final Kind kind;
        final String path;
        final String name;
        final String contextPath;
        /** The folder served as the application, for modules nothing builds. */
        final String webRoot;
        /** Compiled classes mounted at {@code /WEB-INF/classes}, when they are outside the Web root. */
        final String classes;

        Module(Kind kind, String path, String name, String contextPath, String webRoot, String classes) {
            this.kind = kind;
            this.path = path;
            this.name = name;
            this.contextPath = contextPath;
            this.webRoot = webRoot;
            this.classes = classes;
        }
    }

    /**
     * Build files that exist but could not be read or parsed during one
     * request. A module behind one is left out, so the request reports the
     * count once as a degraded result instead of hiding it.
     */
    static final class Unreadable {
        long count;

        void report(CallContext context, String operation) {
            if (count > 0) {
                context.diagnostics().degraded(operation, "buildFileUnreadable", count);
            }
        }
    }

    private final Path workspace;
    private final CallContext context;

    WebModules(Path workspace, CallContext context) {
        this.workspace = workspace;
        this.context = context;
    }

    Map<String, Object> model(Map<String, Object> params) throws IOException {
        Object root = params.get("root");
        Path instance = workspace.resolve(root instanceof String ? (String) root : ".").normalize();
        if (!instance.startsWith(workspace)) {
            throw new PluginError("tomcat.invalidRoot", "The build root is outside the workspace.");
        }
        List<Map<String, Object>> projects = new ArrayList<>();
        List<Map<String, Object>> targets = new ArrayList<>();
        Unreadable unreadable = new Unreadable();
        List<Module> modules = scan(instance, unreadable);
        unreadable.report(context, "lithe/build/model");
        for (Module module : modules) {
            Map<String, Object> properties = new LinkedHashMap<>();
            properties.put("kind", module.kind.id);
            Map<String, Object> project = new LinkedHashMap<>();
            project.put("path", module.path);
            project.put("name", module.name);
            project.put("properties", properties);
            projects.add(project);
            Map<String, Object> targetProperties = new LinkedHashMap<>(properties);
            targetProperties.put("contextPath", module.contextPath);
            if (module.webRoot != null) {
                targetProperties.put("webRoot", module.webRoot);
            }
            Map<String, Object> target = new LinkedHashMap<>();
            target.put("project", module.path);
            target.put("path", module.path);
            target.put("name", module.name);
            target.put("tags", Collections.singletonList(TAG));
            target.put("properties", targetProperties);
            targets.add(target);
        }
        Map<String, Object> model = new LinkedHashMap<>();
        model.put("format", "litheBuildModel");
        model.put("version", 0);
        model.put("projects", projects);
        model.put("targets", targets);
        model.put("tasks", Collections.emptyList());
        model.put("contentRoots", Collections.emptyList());
        model.put("optionValues", Collections.emptyMap());
        return model;
    }

    /** Every Web module below {@code instance}, in directory order. */
    List<Module> scan(Path instance, Unreadable unreadable) throws IOException {
        List<Module> modules = new ArrayList<>();
        Deque<Path> pending = new ArrayDeque<>();
        Deque<Integer> depths = new ArrayDeque<>();
        pending.add(instance);
        depths.add(0);
        int visited = 0;
        while (!pending.isEmpty()) {
            context.throwIfCancelled();
            Path directory = pending.removeFirst();
            int depth = depths.removeFirst();
            if (++visited > MAX_DIRECTORIES) {
                context.diagnostics().degraded("lithe/build/model", "webModuleScanTruncated", (long) MAX_DIRECTORIES);
                break;
            }
            Module module = detect(instance, directory, unreadable);
            if (module != null) {
                modules.add(module);
            }
            if (depth >= MAX_DEPTH) {
                continue;
            }
            List<Path> children = new ArrayList<>();
            try (DirectoryStream<Path> entries = Files.newDirectoryStream(directory)) {
                for (Path child : entries) {
                    String name = child.getFileName().toString();
                    if (!name.startsWith(".") && !SKIPPED.contains(name) && Files.isDirectory(child)
                            && !Files.isSymbolicLink(child) && !isWebRoot(instance, child, module)) {
                        children.add(child);
                    }
                }
            } catch (IOException failed) {
                unreadable.count++;
                continue;
            }
            Collections.sort(children);
            for (Path child : children) {
                pending.add(child);
                depths.add(depth + 1);
            }
        }
        return modules;
    }

    private static boolean isWebRoot(Path instance, Path child, Module module) {
        return module != null && module.webRoot != null && child.equals(instance.resolve(module.webRoot));
    }

    /** The module at {@code directory}, or null when it is none. */
    static Module detect(Path instance, Path directory, Unreadable unreadable) {
        String path = relative(instance, directory);
        String folder = directory.getFileName() == null || path.equals(".")
                ? folderName(directory) : directory.getFileName().toString();
        if (directory.equals(instance) && "WEB-INF".equals(folder) && Files.isRegularFile(directory.resolve("web.xml"))) {
            return webInfRoot(instance, directory);
        }
        Path pom = directory.resolve("pom.xml");
        Path gradle = Files.isRegularFile(directory.resolve("build.gradle.kts"))
                ? directory.resolve("build.gradle.kts") : directory.resolve("build.gradle");
        if (Files.isRegularFile(pom)) {
            Document document = xml(pom);
            if (document == null) {
                unreadable.count++;
                return null;
            }
            if (!"war".equals(childText(document.getDocumentElement(), "packaging"))) {
                return null;
            }
            String artifact = childText(document.getDocumentElement(), "artifactId");
            String name = artifact == null ? folder : artifact;
            return new Module(Kind.MAVEN, path, name, "/" + segment(name), null, null);
        }
        if (Files.isRegularFile(gradle)) {
            String text = read(gradle);
            if (text == null) {
                unreadable.count++;
                return null;
            }
            if (!GRADLE_WAR.matcher(stripComments(text)).find()) {
                return null;
            }
            return new Module(Kind.GRADLE, path, folder, "/" + segment(folder), null, null);
        }
        Module wtp = wtp(instance, directory, path, folder, unreadable);
        if (wtp != null) {
            return wtp;
        }
        for (String candidate : WEB_ROOTS) {
            Path webRoot = directory.resolve(candidate);
            if (Files.isRegularFile(webRoot.resolve("WEB-INF/web.xml"))) {
                String classes = firstDirectory(directory,
                        "out/production/" + folder, "build/classes/java/main", "build/classes", "bin", "classes");
                return new Module(Kind.PLAIN, path, folder, "/" + segment(folder),
                        relative(instance, webRoot), classes == null ? null : join(path, classes));
            }
        }
        return null;
    }

    /**
     * A plain Web folder found by its {@code web.xml} alone. Lithe makes the
     * folder holding the marker, {@code WEB-INF}, the instance root, so the
     * module is that instance: the application is the folder above it, and
     * the project it belongs to is the one above a usual Web-root name
     * ({@code web}, {@code WebContent}, {@code src/main/webapp}...), whose
     * output folder supplies the compiled classes.
     */
    private static Module webInfRoot(Path instance, Path webInf) {
        Path webRoot = webInf.getParent();
        Path project = webRoot;
        for (String candidate : WEB_ROOTS) {
            Path owner = webRoot;
            for (int level = candidate.split("/").length; level > 0 && owner != null; level--) {
                owner = owner.getParent();
            }
            if (owner != null && owner.resolve(candidate).normalize().equals(webRoot)) {
                project = owner;
                break;
            }
        }
        String name = folderName(project);
        String classes = firstDirectory(project, "out/production/" + name, "build/classes/java/main", "build/classes", "bin", "classes");
        return new Module(Kind.PLAIN, ".", name, "/" + segment(name), relative(instance, webRoot),
                classes == null ? null : relative(instance, project.resolve(classes)));
    }

    /** An Eclipse Dynamic Web Project: its component file names the Web root and the context root. */
    private static Module wtp(Path instance, Path directory, String path, String folder, Unreadable unreadable) {
        Path component = directory.resolve(WTP_COMPONENT);
        if (!Files.isRegularFile(component) || !Files.isRegularFile(directory.resolve(".project"))) {
            return null;
        }
        Document document = xml(component);
        if (document == null) {
            unreadable.count++;
            return null;
        }
        NodeList modules = document.getElementsByTagName("wb-module");
        if (modules.getLength() == 0) {
            return null;
        }
        Element module = (Element) modules.item(0);
        String webRoot = null;
        String contextRoot = null;
        for (Node child = module.getFirstChild(); child != null; child = child.getNextSibling()) {
            if (!(child instanceof Element)) {
                continue;
            }
            Element element = (Element) child;
            if ("wb-resource".equals(element.getTagName()) && "/".equals(element.getAttribute("deploy-path")) && webRoot == null) {
                webRoot = trimSlashes(element.getAttribute("source-path"));
            } else if ("property".equals(element.getTagName()) && "context-root".equals(element.getAttribute("name"))) {
                contextRoot = trimSlashes(element.getAttribute("value"));
            }
        }
        if (webRoot == null || webRoot.isEmpty() || webRoot.contains("..") || !Files.isDirectory(directory.resolve(webRoot))) {
            return null;
        }
        String deployName = module.getAttribute("deploy-name");
        String name = deployName == null || deployName.trim().isEmpty() ? folder : deployName.trim();
        String context = contextRoot == null ? segment(name) : contextPath(contextRoot);
        String output = eclipseOutput(directory, unreadable);
        String classes = output != null && Files.isDirectory(directory.resolve(output)) ? join(path, output) : null;
        return new Module(Kind.WTP, path, name, context.isEmpty() ? "" : "/" + context,
                relative(instance, directory.resolve(webRoot)), classes);
    }

    /** The {@code output} entry of an Eclipse {@code .classpath}. */
    private static String eclipseOutput(Path directory, Unreadable unreadable) {
        Path classpath = directory.resolve(".classpath");
        if (!Files.isRegularFile(classpath)) {
            return null;
        }
        Document document = xml(classpath);
        if (document == null) {
            unreadable.count++;
            return null;
        }
        NodeList entries = document.getElementsByTagName("classpathentry");
        for (int index = 0; index < entries.getLength(); index++) {
            Element entry = (Element) entries.item(index);
            if ("output".equals(entry.getAttribute("kind"))) {
                String output = trimSlashes(entry.getAttribute("path"));
                return output.isEmpty() || output.contains("..") ? null : output;
            }
        }
        return null;
    }

    private static String firstDirectory(Path directory, String... candidates) {
        for (String candidate : candidates) {
            if (Files.isDirectory(directory.resolve(candidate))) {
                return candidate;
            }
        }
        return null;
    }

    /** A context root made of safe path segments; empty is the root context. */
    static String contextPath(String value) {
        StringBuilder result = new StringBuilder();
        for (String part : value.split("/")) {
            if (part.isEmpty()) {
                continue;
            }
            if (result.length() > 0) {
                result.append('/');
            }
            result.append(segment(part));
        }
        return result.toString();
    }

    /** One URL path segment Tomcat can also use in a context file name. */
    static String segment(String value) {
        StringBuilder result = new StringBuilder();
        for (char character : value.toCharArray()) {
            result.append(Character.isLetterOrDigit(character) && character < 128 || character == '.' || character == '-'
                    || character == '_' ? character : '_');
        }
        String text = result.toString();
        return text.isEmpty() || text.equals(".") || text.equals("..") ? "app" : text;
    }

    static String relative(Path instance, Path path) {
        String relative = instance.relativize(path).toString().replace('\\', '/');
        return relative.isEmpty() ? "." : relative;
    }

    static String join(String parent, String child) {
        return ".".equals(parent) ? child : parent + "/" + child;
    }

    private static String folderName(Path directory) {
        Path name = directory.toAbsolutePath().normalize().getFileName();
        return name == null ? "app" : name.toString();
    }

    private static String trimSlashes(String value) {
        String text = value == null ? "" : value.trim().replace('\\', '/');
        while (text.startsWith("/")) {
            text = text.substring(1);
        }
        while (text.endsWith("/")) {
            text = text.substring(0, text.length() - 1);
        }
        return text;
    }

    private static String childText(Element parent, String name) {
        for (Node child = parent.getFirstChild(); child != null; child = child.getNextSibling()) {
            if (child instanceof Element && name.equals(localName(child))) {
                String text = child.getTextContent().trim();
                return text.isEmpty() ? null : text;
            }
        }
        return null;
    }

    private static String localName(Node node) {
        String name = node.getLocalName() != null ? node.getLocalName() : node.getNodeName();
        int colon = name.indexOf(':');
        return colon < 0 ? name : name.substring(colon + 1);
    }

    /** Drops line and block comments so a commented-out {@code war} plugin does not count. */
    static String stripComments(String text) {
        return text.replaceAll("(?s)/\\*.*?\\*/", "").replaceAll("(?m)//.*$", "");
    }

    private static String read(Path file) {
        try {
            if (Files.size(file) > MAX_BUILD_FILE) {
                return null;
            }
            return new String(Files.readAllBytes(file), StandardCharsets.UTF_8);
        } catch (IOException unreadable) {
            return null;
        }
    }

    /** A parsed XML file, or null when it cannot be read; no DTD or external entity is loaded. */
    static Document xml(Path file) {
        try (InputStream input = Files.newInputStream(file)) {
            if (Files.size(file) > MAX_BUILD_FILE) {
                return null;
            }
            DocumentBuilderFactory factory = DocumentBuilderFactory.newInstance();
            factory.setNamespaceAware(true);
            factory.setExpandEntityReferences(false);
            factory.setFeature("http://apache.org/xml/features/disallow-doctype-decl", true);
            factory.setFeature("http://xml.org/sax/features/external-general-entities", false);
            factory.setFeature("http://xml.org/sax/features/external-parameter-entities", false);
            DocumentBuilder builder = factory.newDocumentBuilder();
            // Report a malformed file as an exception, not as text on standard error.
            builder.setErrorHandler(new org.xml.sax.helpers.DefaultHandler());
            return builder.parse(input);
        } catch (Exception unreadable) {
            return null;
        }
    }
}
