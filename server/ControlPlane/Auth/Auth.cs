// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

using System.ComponentModel.DataAnnotations;
using Microsoft.AspNetCore.Authentication.JwtBearer;
using Microsoft.AspNetCore.Authorization;
using Microsoft.Extensions.Options;
using Tyger.Common.Api;

namespace Tyger.ControlPlane.Auth;

public static class Auth
{
    private const string OwnerRoleName = "owner";
    private const string OwnerPolicyName = "owner";
    private const string ContributorRoleName = "contributor";
    private const string AtLeastContributorPolityName = "contributor";

    private static readonly Dictionary<string, string[]> s_policyToSatisfyingRoles = new()
    {
        { OwnerPolicyName, [ OwnerRoleName ] },
        { AtLeastContributorPolityName, [ ContributorRoleName, OwnerRoleName ] },
    };

    public static void AddAuth(this IHostApplicationBuilder builder)
    {
        builder.Services.AddOptions<AuthOptions>().BindConfiguration("auth", o => o.ErrorOnUnknownConfiguration = true).ValidateDataAnnotations().ValidateOnStart();

        builder.Services.AddAuthentication(JwtBearerDefaults.AuthenticationScheme).AddJwtBearer(options =>
        {
            options.Events = new JwtBearerEvents
            {
                OnForbidden = async context =>
                {
                    var result = Responses.Forbidden("Insufficient permissions to perform this operation");
                    await result.ExecuteAsync(context.HttpContext);
                },
            };
        });

        builder.Services.AddOptions<JwtBearerOptions>(JwtBearerDefaults.AuthenticationScheme).Configure<IOptions<AuthOptions>>((jwtOptions, securityConfiguration) =>
        {
            if (securityConfiguration.Value.Enabled)
            {
                // Tokens using the v2 format use the v2.0 endpoint
                jwtOptions.Authority = securityConfiguration.Value.Authority + "/v2.0";
                jwtOptions.Audience = securityConfiguration.Value.Audience;
                jwtOptions.Challenge = $"Bearer authority={securityConfiguration.Value.Authority}, audience={securityConfiguration.Value.Audience}";
            }
        });

        builder.Services.AddAuthorization();
        builder.Services.AddOptions<AuthorizationOptions>().Configure<IOptions<AuthOptions>>((authOptions, securityConfigurations) =>
        {
            bool authPoliciesAdded = false;
            if (securityConfigurations.Value.Enabled)
            {
                authOptions.FallbackPolicy = new AuthorizationPolicyBuilder().RequireAuthenticatedUser().Build();

                if (securityConfigurations.Value.AccessControl.Enabled)
                {
                    foreach ((var policy, var satisfyingRoles) in s_policyToSatisfyingRoles)
                    {
                        authOptions.AddPolicy(policy, builder =>
                        {
                            builder.RequireAuthenticatedUser();
                            builder.RequireRole(satisfyingRoles);
                        });
                    }

                    authPoliciesAdded = true;
                }
            }

            if (!authPoliciesAdded)
            {
                foreach (var policy in s_policyToSatisfyingRoles)
                {
                    authOptions.AddPolicy(policy.Key, builder =>
                    {
                        if (securityConfigurations.Value.AccessControl.Enabled)
                        {
                            builder.RequireAuthenticatedUser();
                        }

                        builder.RequireAssertion(context => true);
                    });
                }
            }
        });
    }

    public static void UseAuth(this WebApplication app)
    {
        if (app.Services.GetRequiredService<IOptions<AuthOptions>>().Value.Enabled)
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
}

public class AuthOptions : IValidatableObject
{
    public bool Enabled { get; set; } = true;
    public string? Authority { get; init; }
    public string? Audience { get; init; }
    public string? ApiAppUri { get; init; }
    public string? ApiAppId { get; init; }
    public string? CliAppUri { get; init; }
    public string? CliAppId { get; init; }

    public AccessControlOptions AccessControl { get; init; } = new();

    public IEnumerable<ValidationResult> Validate(ValidationContext validationContext)
    {
        if (!Enabled)
        {
            yield break;
        }

        if (string.IsNullOrWhiteSpace(Authority) || string.IsNullOrWhiteSpace(Audience))
        {
            yield return new ValidationResult("When security is enabled, Authority, Audience, and CliAppUri must be specified");
        }
    }
}

public class AccessControlOptions
{
    public bool Enabled { get; set; } = true;
}
