package io.github.litheidea.tomcat;

import io.github.litheidea.plugin.sdk.Backend;
import io.github.litheidea.plugin.sdk.Json;
import io.github.litheidea.plugin.sdk.PluginError;
import java.net.URI;
import java.nio.file.Path;
import java.nio.file.Paths;
import java.util.Collections;
import java.util.Map;

/**
 * The Tomcat build provider process. Lithe starts it on the first build
 * request of a Tomcat build instance and talks to it over stdio:
 * {@code lithe/build/model} lists the Web modules of the instance, and
 * {@code lithe/build/prepareRun} builds one module and returns the launch of
 * a Tomcat whose private CATALINA_BASE serves it. Lithe starts, stops and
 * debugs that process; this one never owns a server.
 *
 * <p>Arguments: {@code --state <dir>}, the workspace state directory Lithe
 * gives the build system, which holds the generated CATALINA_BASE folders.
 * Settings arrive as {@code initializationOptions}.
 */
public final class TomcatProvider {
    private TomcatProvider() {}

    public static void main(String[] args) throws Exception {
        Path state = state(args);
        Backend backend = Backend.create("lithe-tomcat-provider", TomcatProvider.class.getPackage().getImplementationVersion());
        backend.onModel((params, context) -> new WebModules(workspace(backend), context).model(params));
        backend.onPrepareRun((params, context) -> new Preparation(workspace(backend), state, settings(backend), messages(backend))
                .prepare(params, context));
        // The model is read again whenever a watched build file changes; its
        // success is routine, and a failure or a degraded scan is still logged.
        backend.suppressSuccessDiagnostics("lithe/build/model");
        backend.listen();
    }

    private static Path state(String[] args) {
        for (int index = 0; index + 1 < args.length; index++) {
            if ("--state".equals(args[index])) {
                return Paths.get(args[index + 1]).toAbsolutePath().normalize();
            }
        }
        throw new IllegalArgumentException("usage: --state <workspace state directory>");
    }

    /** The workspace folder Lithe sent in {@code initialize}. */
    static Path workspace(Backend backend) {
        Map<String, Object> initialize = backend.initializeParams();
        Object root = initialize == null ? null : initialize.get("rootUri");
        if (!(root instanceof String)) {
            throw new PluginError("tomcat.noWorkspace", "Lithe sent no workspace folder.");
        }
        return Paths.get(URI.create((String) root)).toAbsolutePath().normalize();
    }

    private static Settings settings(Backend backend) {
        Map<String, Object> initialize = backend.initializeParams();
        Object options = initialize == null ? null : initialize.get("initializationOptions");
        return Settings.of(options instanceof Map ? Json.object(options) : Collections.<String, Object>emptyMap());
    }

    private static Messages messages(Backend backend) {
        Map<String, Object> initialize = backend.initializeParams();
        Object locale = initialize == null ? null : initialize.get("locale");
        return new Messages(locale instanceof String ? (String) locale : null);
    }
}
