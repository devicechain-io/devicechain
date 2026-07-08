// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System.Collections.Generic;

namespace DeviceChain.Sdk.Auth;

/// <summary>A tenant an authenticated identity belongs to, with the role tokens it holds.</summary>
public sealed class Membership
{
    public string Tenant { get; set; } = "";
    public List<string> Roles { get; set; } = new();
}

/// <summary>
/// The result of an email/password <c>login</c> (ADR-033): an instance-scoped identity
/// token plus the tenants the identity may act in. The caller picks one via selectTenant.
/// </summary>
public sealed class IdentityAuth
{
    public string IdentityToken { get; set; } = "";
    public string ExpiresAt { get; set; } = "";
    public bool Superuser { get; set; }
    public List<Membership> Memberships { get; set; } = new();
}

/// <summary>A tenant-scoped pair of JWTs returned by <c>selectTenant</c> or <c>refresh</c>.</summary>
public sealed class AuthToken
{
    public string AccessToken { get; set; } = "";
    public string RefreshToken { get; set; } = "";
    public string ExpiresAt { get; set; } = "";
}

// ── GraphQL operation envelopes (data + variables), concrete for source-gen JSON ──

internal sealed class LoginVariables
{
    public string Email { get; set; } = "";
    public string Password { get; set; } = "";
}

internal sealed class LoginData
{
    public IdentityAuth Login { get; set; } = new();
}

internal sealed class SelectTenantVariables
{
    public string IdentityToken { get; set; } = "";
    public string Tenant { get; set; } = "";
}

internal sealed class SelectTenantData
{
    public AuthToken SelectTenant { get; set; } = new();
}

internal sealed class RefreshVariables
{
    public string RefreshToken { get; set; } = "";
}

internal sealed class RefreshData
{
    public AuthToken Refresh { get; set; } = new();
}
