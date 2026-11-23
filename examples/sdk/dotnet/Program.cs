using System.Diagnostics;
using Microsoft.AspNetCore.Hosting.Server;
using Microsoft.AspNetCore.Hosting.Server.Features;
using OpenTelemetry;
using OpenTelemetry.Resources;
using OpenTelemetry.Trace;

using var source = new ActivitySource("geko.sdk.dotnet");
using var provider = Sdk.CreateTracerProviderBuilder()
    .AddSource(source.Name)
    .SetResourceBuilder(ResourceBuilder.CreateDefault())
    .AddOtlpExporter()
    .Build();
var builder = WebApplication.CreateBuilder(args);
builder.Logging.ClearProviders();
builder.WebHost.UseUrls("http://127.0.0.1:0");
await using var app = builder.Build();
foreach (var scenario in new[] { "success", "failure" })
{
    app.MapGet("/" + scenario, () =>
    {
        using var span = source.StartActivity("GET /" + scenario, ActivityKind.Server);
        span?.SetTag("http.request.method", "GET");
        span?.SetTag("http.route", "/" + scenario);
        var failing = scenario == "failure";
        if (failing)
        {
            using var child = source.StartActivity("payment.authorize");
            try { throw new InvalidOperationException("Demo payment gateway unavailable"); }
            catch (InvalidOperationException error)
            {
                child?.AddEvent(new ActivityEvent("exception", tags: new ActivityTagsCollection
                {
                    ["exception.type"] = error.GetType().FullName,
                    ["exception.message"] = error.Message,
                    ["exception.stacktrace"] = error.ToString(),
                }));
                child?.SetStatus(ActivityStatusCode.Error);
            }
        }
        var status = failing ? 500 : 200;
        span?.SetTag("http.response.status_code", status);
        if (failing) span?.SetStatus(ActivityStatusCode.Error);
        return Results.Json(new { scenario, status, trace_id = span?.TraceId.ToHexString() }, statusCode: status);
    });
}
try
{
    await app.StartAsync();
    var address = app.Services.GetRequiredService<IServer>().Features.Get<IServerAddressesFeature>()!.Addresses.Single();
    using var client = new HttpClient { BaseAddress = new Uri(address), Timeout = TimeSpan.FromSeconds(10) };
    foreach (var scenario in new[] { "success", "failure" })
    {
        using var response = await client.GetAsync("/" + scenario);
        if ((int)response.StatusCode != (scenario == "failure" ? 500 : 200)) throw new InvalidOperationException("Unexpected example response");
        Console.WriteLine(await response.Content.ReadAsStringAsync());
    }
}
finally
{
    using var stop = new CancellationTokenSource(TimeSpan.FromSeconds(10));
    try { await app.StopAsync(stop.Token); }
    finally
    {
        if (!provider.ForceFlush(10000)) throw new InvalidOperationException("Trace flush did not complete");
    }
}
