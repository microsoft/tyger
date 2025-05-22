// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

using System.ComponentModel.DataAnnotations;
using Microsoft.AspNetCore.Authentication.JwtBearer;
using Microsoft.AspNetCore.Authorization;
using Microsoft.Extensions.Options;
using Microsoft.IdentityModel.JsonWebTokens;
using Microsoft.Net.Http.Headers;
using Tyger.Common.Api;

namespace Tyger.ControlPlane.AccessControl;

public static class AccessControl
{
    private const string OwnerRoleName = "owner";
    private const string OwnerPolicyName = "owner";
    private const string ContributorRoleName = "contributor";
    private const string AtLeastContributorPolityName = "contributor";

    private const string EntraV1Scheme = "v1-entra-jwt";
    private const string EntraV2Scheme = "v2-entra-jwt";

    private const string DualEntraScheme = "dual-entra-jwt";

    // The following policies are used to determine which roles are required to satisfy the policy.
    private static readonly Dictionary<string, string[]> s_policyToSatisfyingRoles = new()
    {
        { OwnerPolicyName, [ OwnerRoleName ] },
        { AtLeastContributorPolityName, [ ContributorRoleName, OwnerRoleName ] },
    };

    public static void AddAccessControl(this IHostApplicationBuilder builder)
    {
        builder.Services.AddOptions<AccessControlOptions>().BindConfiguration("accessControl", o => o.ErrorOnUnknownConfiguration = true).ValidateDataAnnotations().ValidateOnStart();

        builder.Services.AddAuthentication(options =>
            {
                options.DefaultAuthenticateScheme = DualEntraScheme;
                options.DefaultChallengeScheme = DualEntraScheme;
            })
            .AddPolicyScheme(DualEntraScheme, null, options =>
            {
                options.ForwardDefaultSelector = context =>
                {
                    string? authorization = context.Request.Headers[HeaderNames.Authorization];
                    if (!string.IsNullOrEmpty(authorization) && authorization.StartsWith("Bearer ", StringComparison.Ordinal))
                    {
                        var token = authorization["Bearer ".Length..].Trim();
                        var jwtHandler = new JsonWebTokenHandler();

                        if (jwtHandler.CanReadToken(token) &&
                            jwtHandler.ReadJsonWebToken(token).TryGetValue("ver", out string version))
                        {
                            switch (version)
                            {
                                case "1.0":
                                    return EntraV1Scheme;
                                case "2.0":
                                    return EntraV2Scheme;
                            }
                        }
                    }

                    return EntraV1Scheme;
                };
            })
            .AddJwtBearer(EntraV1Scheme)
            .AddJwtBearer(EntraV2Scheme);

        builder.Services.AddOptions<JwtBearerOptions>(EntraV1Scheme).Configure<IOptions<AccessControlOptions>>((jwtOptions, securityConfiguration) =>
        {
            if (securityConfiguration.Value.Enabled)
            {
                // Tokens using the v1 format use the v1.0 endpoint
                jwtOptions.Authority = securityConfiguration.Value.Authority;
                jwtOptions.Audience = securityConfiguration.Value.Audience;
                jwtOptions.Challenge = $"Bearer authority={securityConfiguration.Value.Authority}, audience={securityConfiguration.Value.Audience}";
                jwtOptions.Events = new() { OnForbidden = OnForbidden };
            }
        });

        builder.Services.AddOptions<JwtBearerOptions>(EntraV2Scheme).Configure<IOptions<AccessControlOptions>>((jwtOptions, securityConfiguration) =>
        {
            if (securityConfiguration.Value.Enabled)
            {
                // Tokens using the v2 format use the v2.0 endpoint
                jwtOptions.Authority = securityConfiguration.Value.Authority + "/v2.0";
                jwtOptions.Audience = securityConfiguration.Value.ApiAppId;
                jwtOptions.Challenge = $"Bearer authority={securityConfiguration.Value.Authority}, audience={securityConfiguration.Value.Audience}";
                jwtOptions.Events = new() { OnForbidden = OnForbidden };
            }
        });

        builder.Services.AddAuthorization();
        builder.Services.AddOptions<AuthorizationOptions>().Configure<IOptions<AccessControlOptions>>((authOptions, securityConfigurations) =>
        {
            if (securityConfigurations.Value.Enabled)
            {
                authOptions.FallbackPolicy = new AuthorizationPolicyBuilder().RequireAuthenticatedUser().Build();
                foreach ((var policy, var satisfyingRoles) in s_policyToSatisfyingRoles)
                {
                    authOptions.AddPolicy(policy, builder =>
                    {
                        builder.RequireAuthenticatedUser();
                        builder.RequireRole(satisfyingRoles);
                    });
                }
            }
            else
            {
                foreach (var policy in s_policyToSatisfyingRoles)
                {
                    authOptions.AddPolicy(policy.Key, builder =>
                    {
                        builder.RequireAssertion(context => true);
                    });
                }
            }
        });
    }

    public static void UseAuth(this WebApplication app)
    {
        if (app.Services.GetRequiredService<IOptions<AccessControlOptions>>().Value.Enabled)
        {
            app.UseAuthentication();
            app.UseAuthorization();
        }
    }

    public static TBuilder RequireAtLeastContributorRole<TBuilder>(this TBuilder builder) where TBuilder : IEndpointConventionBuilder
    {
        return builder.RequireAuthorization(AtLeastContributorPolityName);
    }

    public static TBuilder RequireOwnerRole<TBuilder>(this TBuilder builder) where TBuilder : IEndpointConventionBuilder
    {
        return builder.RequireAuthorization(OwnerPolicyName);
    }

    private static async Task OnForbidden(ForbiddenContext context)
    {
        var result = Responses.Forbidden("Insufficient permissions to perform this operation");
        await result.ExecuteAsync(context.HttpContext);
    }
}

public class AccessControlOptions : IValidatableObject
{
    public bool Enabled { get; set; } = true;
    public string? Authority { get; init; }
    public string? Audience { get; init; }
    public string? ApiAppUri { get; init; }
    public string? ApiAppId { get; init; }
    public string? CliAppUri { get; init; }
    public string? CliAppId { get; init; }

    public IEnumerable<ValidationResult> Validate(ValidationContext validationContext)
    {
        if (string.IsNullOrWhiteSpace(Authority) || string.IsNullOrWhiteSpace(Audience))
        {
            yield return new ValidationResult("When security is enabled, Authority, Audience, and CliAppUri must be specified");
        }
    }
}
