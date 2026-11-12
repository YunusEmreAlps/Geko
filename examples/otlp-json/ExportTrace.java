import java.net.URI;
import java.net.http.HttpClient;
import java.net.http.HttpRequest;
import java.net.http.HttpResponse;
import java.nio.file.Files;
import java.nio.file.Path;
import java.time.Duration;
import java.util.UUID;

class ExportTrace {
    public static void main(String[] args) throws Exception {
        String endpoint = System.getenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT");
        if (endpoint == null || endpoint.isEmpty()) endpoint = "http://127.0.0.1:4318/v1/traces";
        String traceId = UUID.randomUUID().toString().replace("-", "");
        long end = System.currentTimeMillis() * 1_000_000;
        String body = Files.readString(Path.of("examples", "otlp-json", "trace.json"))
            .replace("@SERVICE@", "java-inventory").replace("@TRACE_ID@", traceId)
            .replace("@START@", Long.toString(end - 1_000_000)).replace("@END@", Long.toString(end));
        HttpRequest request = HttpRequest.newBuilder(URI.create(endpoint))
            .timeout(Duration.ofSeconds(5)).header("Content-Type", "application/json")
            .POST(HttpRequest.BodyPublishers.ofString(body)).build();
        var response = HttpClient.newHttpClient().send(request, HttpResponse.BodyHandlers.ofString());
        if (response.statusCode() != 200 || !response.body().strip().equals("{}")) {
            throw new IllegalStateException("Export failed (" + response.statusCode() + "): " + response.body());
        }
        System.out.println(traceId);
    }
}
