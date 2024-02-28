// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

using System.ComponentModel.DataAnnotations;
using System.Text.RegularExpressions;

using Microsoft.AspNetCore.Mvc;
using Microsoft.AspNetCore.WebUtilities;
using Microsoft.Extensions.Primitives;
using Tyger.ControlPlane.Database;
using Tyger.ControlPlane.Json;
using Tyger.ControlPlane.Model;

namespace Tyger.ControlPlane.Codespecs;

public static class Codespecs
{
    public static void MapCodespecs(this WebApplication app)
    {
        app.MapPut("/v1/codespecs/{name}", async (string name, IRepository repository, HttpContext context) =>
        {
            string pattern = @"^[a-z0-9\-._]*$";
            if (!Regex.IsMatch(name, pattern))
            {
                throw new ValidationException("Codespec names must contain only lower case letters (a-z), numbers (0-9), dashes (-), underscores (_), and dots (.)");
            }

            var newCodespec = await context.Request.ReadAndValidateJson<Codespec>(context.RequestAborted);

            if (!string.IsNullOrEmpty(newCodespec.Name) && newCodespec.Name != name)
            {
                throw new ValidationException("If provided, the codespec name in the body must match the name in the URL.");
            }

            var codespec = await repository.UpsertCodespec(name, newCodespec!, context.RequestAborted);
            context.Response.Headers.Location = $"/v1/codespecs/{name}/{codespec.Version}";
            return Results.Json(codespec, statusCode: codespec.Version == 1 ? StatusCodes.Status201Created : StatusCodes.Status200OK);
        })
        .Accepts<Codespec>("application/json")
        .Produces<Codespec>(StatusCodes.Status200OK)
        .Produces<Codespec>(StatusCodes.Status201Created)
        .Produces<ErrorBody>(StatusCodes.Status400BadRequest);

        app.MapGet("/v1/codespecs/{name}", async (string name, IRepository repository, HttpContext context) =>
        {
            Codespec? codespec = await repository.GetLatestCodespec(name, context.RequestAborted);
            if (codespec == null)
            {
                return Responses.NotFound();
            }

            context.Response.Headers.Location = $"/v1/codespecs/{name}/{codespec.Version}";
            return Results.Ok(codespec);
        })
        .Produces<Codespec>();

        app.MapGet("/v1/codespecs", async (IRepository repository, int? limit, string? prefix, [FromQuery(Name = "_ct")] string? continuationToken, HttpContext context) =>
        {
            limit = limit is null ? 20 : Math.Min(limit.Value, 200);
            (var codespecs, var nextContinuationToken) = await repository.GetCodespecs(limit.Value, prefix, continuationToken, context.RequestAborted);

            string? nextLink;
            if (nextContinuationToken is null)
            {
                nextLink = null;
            }
            else if (context.Request.QueryString.HasValue)
            {
                var qd = QueryHelpers.ParseQuery(context.Request.QueryString.Value);
                qd["_ct"] = new StringValues(nextContinuationToken);
                nextLink = QueryHelpers.AddQueryString(context.Request.Path, qd);
            }
            else
            {
                nextLink = QueryHelpers.AddQueryString(context.Request.Path, "_ct", nextContinuationToken);
            }

            return Results.Ok(new CodespecPage(codespecs, nextLink == null ? null : new Uri(nextLink)));
        })
        .Produces<CodespecPage>();

        app.MapGet("/v1/codespecs/{name}/versions/{version}", async (string name, string version, IRepository repository, CancellationToken cancellationToken) =>
        {
            if (!int.TryParse(version, out var versionInt))
            {
                return Responses.NotFound();
            }

            var codespec = await repository.GetCodespecAtVersion(name, versionInt, cancellationToken);
            if (codespec == null)
            {
                return Responses.NotFound();
            }

            return Results.Ok(codespec);
        })
        .Produces<Codespec>();
    }
}
