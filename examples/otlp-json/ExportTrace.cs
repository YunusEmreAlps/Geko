#:property PublishAot=false

using System;
using System.IO;
using System.Net.Http;
using System.Text;
using System.Text.Json;

var endpoint = Environment.GetEnvironmentVariable("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT");
if (string.IsNullOrEmpty(endpoint)) endpoint = "http://127.0.0.1:4318/v1/traces";
var traceId = Guid.NewGuid().ToString("N");
var end = DateTimeOffset.UtcNow.ToUnixTimeMilliseconds() * 1_000_000;
var fixture = await File.ReadAllTextAsync(Path.Combine("examples", "otlp-json", "trace.json"));
var body = fixture.Replace("@SERVICE@", "dotnet-inventory").Replace("@TRACE_ID@", traceId)
    .Replace("@START@", (end - 1_000_000).ToString()).Replace("@END@", end.ToString());
using var client = new HttpClient { Timeout = TimeSpan.FromSeconds(5) };
using var content = new StringContent(body, Encoding.UTF8, "application/json");
using var response = await client.PostAsync(endpoint, content);
response.EnsureSuccessStatusCode();
using var result = JsonDocument.Parse(await response.Content.ReadAsStringAsync());
if (result.RootElement.TryGetProperty("partialSuccess", out var partial))
    throw new InvalidOperationException($"Partial export: {partial}");
Console.WriteLine(traceId);
