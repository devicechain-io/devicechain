---
title: Provisioning and trust anchors
sidebar_position: 12.5
---

# Provisioning and trust anchors

This chapter is for **tenant administrators** who bring fleets of devices onto DeviceChain without
creating and configuring every device by hand. It covers three things under **Connect** in the
sidebar and on the device page:

- **Provisioning Profiles**: the shared entry point a fleet registers itself through.
- **Trust Anchors**: the certificate authorities your tenant trusts to vouch for device certificates.
- **Certificate credentials**: a device that signs in with a client certificate instead of a token
  or password.

Both pages need the `device:read` permission to open and `device:write` to change anything. A member
limited to some of the tenant's customers or groups does not see them.

:::note What ships today
Certificate authentication is arriving in stages. In the console you can create trust anchors, add
certificate credentials to devices and create certificate provisioning profiles. A device whose
certificate is registered as a credential can connect to the MQTT broker with it. A certificate that
no credential names yet is **refused**: a device does not register itself by presenting a
certificate for the first time. See [What works today with certificates](#certificates-today).
:::

## Provisioning profiles {#provisioning-profiles}

A **provisioning profile** lets a fleet register itself. Every unit comes off the line with the same
key and secret, and introduces itself the first time it powers on; you do not create each device
first. This chapter is the console side. How the firmware makes the request, and what it can be
answered, is in [Device self-registration](/guides/device-self-registration).

Open **Connect → Provisioning Profiles**. The list shows each profile's name, token, device type and
strategy, how many devices it has registered (against its limit, if it has one), and its status, with
badges for a profile that is **Disabled**, **Expired**, or marked **Rotate required**.

### Creating a profile {#creating-a-profile}

Choose **New provisioning profile**, then fill in:

- **Name** and **Description**, as for any other entity.
- **Authenticates by**: **Shared secret**, or **Certificate** (see
  [Certificate provisioning profiles](#certificate-profiles)). It is chosen when you create the profile.
- **Provision key** (shared secret only): the public name devices register under. It is unique within
  the tenant and is stored exactly as typed. It is not secret.
- **Device type**: the type of every device the profile creates, and the only type it will adopt.
- **Strategy**: **Allow new devices** creates a device the platform has never heard of, and also adopts
  one you expected for this profile. **Only pre-registered devices** registers only devices you created
  beforehand and expected for this profile; an unknown device is refused.
- **Device limit**: the most devices this profile may register. Leave it empty for no limit. Lowering it
  only refuses further registrations; it removes nothing.
- **Expires**: after this time the profile refuses new registrations. Devices already registered are
  unaffected. Leave it empty for never.
- **Accepting registrations**: turn it off to refuse new registrations at once.

Choose **Create provisioning profile**. For a shared-secret profile the next dialog is
**Copy the provision secret now**. It shows the provision key and the secret the server generated.
**The secret is shown once.** The platform keeps only a one-way digest, so it cannot show the secret
again; if you lose it, rotate it. Flash the key and secret into the fleet's firmware.

### Following a profile {#following-a-profile}

Select a profile to open it. The **Registration** panel counts the **Registered devices** (those
holding a credential this profile issued, revoked ones included) and the **Devices expected for this
profile** (devices created for it that have not necessarily registered yet). **View devices** opens the
device list filtered to either set.

From the profile page you can also:

- **Rotate secret**: replaces the secret. The old one stops working at once, with no overlap, and the
  new one is shown once. Devices already registered keep their credentials. A profile whose secret was
  chosen before the platform generated secrets is marked **Rotate required** and refuses every
  registration until you rotate it.
- **Revoke devices**: disables the credentials of the devices registered through the profile and ends
  their live connections. They are refused until you reset each one's provisioning. Do this, and rotate
  the secret, if the secret may have leaked.
- **Delete**: refused while any device still holds a credential the profile issued. Revoke the devices
  first, then reset their provisioning or delete them.

### Expecting a device {#expecting-a-device}

A profile adopts an existing device only when you expected it for that profile. Set **Expected
provisioning profile** when you create the device, in the device form; the choice lists the profiles for
the device's type. It can only be set when the device is created. The reasons, and the CSV and API
equivalents, are in [Earmarking](/guides/device-self-registration#earmarking).

A device's page shows how it came onto the platform and offers **Reset provisioning**. Use it to let a
revoked device, or one that lost its credential, register again.

## Trust anchors {#trust-anchors}

A **trust anchor** is a certificate authority (CA) certificate. A device certificate is accepted only
if its chain leads to a trust anchor you have registered. Open **Connect → Trust Anchors**. The list
shows each anchor's subject, expiry and how many credentials and profiles use it, with badges for an
anchor that is **Disabled**, **Expired** or **Expires in N d**.

### Adding an anchor {#adding-an-anchor}

Choose **New trust anchor**, give it a token and a name, and supply the **CA certificate (PEM)**: paste
it, or choose the file.

- Supply exactly one certificate. It can be a root, or an intermediate if you want to trust one sub-CA
  only.
- Supply the certificate only, **never a private key**. The form refuses text that contains one.
- A certificate cannot be changed afterwards. Another CA is another anchor.

A tenant may hold up to 32 trust anchors. If the server refuses the certificate, the form says why in a
sentence, for example that it is not a certificate.

### Looking at, disabling and deleting an anchor {#managing-an-anchor}

Select an anchor to see its SHA-256 fingerprint, subject, issuer, the dates it is valid between, and an
**In use** panel with the number of credentials and provisioning profiles that name it.

- **Disable** stops trusting the anchor. The console warns you first: every device whose certificate
  chains to it stops authenticating and is **disconnected now**, and cannot reconnect until you enable
  the anchor again.
- **Enable** reverses that.
- **Delete** is refused while a credential or a profile still names the anchor. The console explains
  the counts. Remove or re-point those first, or disable the anchor to stop trusting it.

## Certificate credentials on a device {#certificate-credentials}

A device can hold a credential that is a certificate. Open the device, then its **Credentials** tab
(see [Credentials](/user-guide/devices#credentials)). Under **Type** choose **X509_CERTIFICATE** and:

1. Paste the **Device certificate (PEM)**. It is the device's own certificate, optionally followed by
   the intermediate certificates that link it to the trust anchor. Certificates only, never a private
   key.
2. Select the **Trust anchor** the chain must reach. Only anchors that are enabled and not expired are
   offered.
3. Choose **Add certificate credential**.

The platform verifies the chain now, and refuses a certificate whose chain cannot authenticate. It
then computes the certificate's **SHA-256 pin**, which becomes the credential's id, and shows it back
as **Certificate pinned** with the common name and the date until which it is valid. The pin is public;
it identifies the certificate and is not a secret.

Each row on the tab shows the common name, the pin, the anchor and the earliest expiry across the
certificate and its chain, flagged when it is near or past. A device can hold at most **three**
certificate credentials; the console says so before you try a fourth. A certificate credential with no
trust anchor cannot authenticate; remove it and add it again.

## Certificate provisioning profiles {#certificate-profiles}

A profile whose **Authenticates by** is **Certificate** names a trust anchor instead of holding a
secret. There is nothing to issue, show or rotate. The device's token is derived from its certificate:
`pv-` followed by the certificate's common name, which is the device's external id.

That rule shows where you expect a device for such a profile. In the device form, set the **External
id** to the certificate's common name and leave the token empty, or set the token to the derived
value. A device expected for a certificate profile cannot change its external id afterwards. A tenant
may hold up to eight certificate profiles.

## What works today with certificates {#certificates-today}

- **A registered certificate connects.** A device connects to the MQTT broker over TLS and presents
  its client certificate. Its MQTT username is `{tenant}:@x509` with no password, and its client id is
  the one the platform gives that device. The platform looks up the certificate's pin, checks the chain
  against the credential's trust anchor at every connect, and ends the session no later than the
  earliest expiry in the chain. Disabling a credential, an anchor or the device refuses the very next
  connect.
- **Self-registration by certificate is not live yet.** A certificate that no credential pins is
  refused. Until registration on first connect ships, add each device's certificate as a credential
  yourself, as above.
- **It needs the DeviceChain broker.** The default broker asks each device for a client certificate.
  An instance rolled back to the upstream broker image keeps token and password devices working and
  refuses certificate devices. See
  [Releases and upgrades](/deployment/releases-and-upgrades).
- **Watching it.** The `DeviceX509ChainFailuresSustained` and related alerts are described in
  [Observability](/deployment/observability).

## Related

- [Device self-registration](/guides/device-self-registration): the firmware side, error codes and
  recovering from a leaked secret.
- [Device credentials](/guides/device-credentials)
- [Devices](/user-guide/devices): the device page and its Credentials tab.
- [Inbound integrations](/user-guide/inbound-integrations): senders that are not devices themselves.
- [Audit](/user-guide/audit): registrations and credential changes are recorded.
