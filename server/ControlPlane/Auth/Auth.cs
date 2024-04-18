// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

using System.ComponentModel.DataAnnotations;
using Microsoft.AspNetCore.Authentication.JwtBearer;
using Microsoft.AspNetCore.Authorization;
using Microsoft.Extensions.Options;

namespace Tyger.ControlPlane.Auth;

public static class Auth
{
    public static void AddAuth(this IHostApplicationBuilder builder)
    {
        builder.Services.AddOptions<AuthOptions>().BindConfiguration("auth", o => o.ErrorOnUnknownConfiguration = true).ValidateDataAnnotations().ValidateOnStart();

        builder.Services.AddAuthentication(JwtBearerDefaults.AuthenticationScheme).AddJwtBearer();
        builder.Services.AddOptions<JwtBearerOptions>(JwtBearerDefaults.AuthenticationScheme).Configure<IOptions<AuthOptions>>((jwtOptions, securityConfiguration) =>
        {
            if (securityConfiguration.Value.Enabled)
            {
                jwtOptions.Authority = securityConfiguration.Value.Authority;
                jwtOptions.Audience = securityConfiguration.Value.Audience;
                jwtOptions.Challenge = $"Bearer authority={securityConfiguration.Value.Authority}, audience={securityConfiguration.Value.Audience}";
            }
        });

        builder.Services.AddAuthorization();
        builder.Services.AddOptions<AuthorizationOptions>().Configure<IOptions<AuthOptions>>((authOptions, securityConfigurations) =>
        {
            if (securityConfigurations.Value.Enabled)
            {
                authOptions.FallbackPolicy = new AuthorizationPolicyBuilder().RequireAuthenticatedUser().Build();
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
}

public class AuthOptions : IValidatableObject
{
    public bool Enabled { get; set; } = true;
    public string? Authority { get; init; }
    public string? Audience { get; init; }
    public string? CliAppUri { get; init; }

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
