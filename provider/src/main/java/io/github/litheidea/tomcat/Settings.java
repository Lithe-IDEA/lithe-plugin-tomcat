package io.github.litheidea.tomcat;

import java.util.Map;

/**
 * The plugin settings Lithe passes in {@code initializationOptions}. Lithe
 * restarts the provider when one changes, so they are fixed for a process.
 */
final class Settings {
    static final int DEFAULT_HTTP_PORT = 8080;

    /** CATALINA_HOME chosen in the settings; empty means "use CATALINA_HOME". */
    final String tomcatHome;
    final int httpPort;

    private Settings(String tomcatHome, int httpPort) {
        this.tomcatHome = tomcatHome;
        this.httpPort = httpPort;
    }

    static Settings of(Map<String, Object> options) {
        Object home = options.get("tomcatHome");
        Object port = options.get("httpPort");
        int httpPort = port instanceof Number ? ((Number) port).intValue() : DEFAULT_HTTP_PORT;
        if (httpPort < 1 || httpPort > 65535) {
            httpPort = DEFAULT_HTTP_PORT;
        }
        return new Settings(home instanceof String ? (String) home : "", httpPort);
    }
}
