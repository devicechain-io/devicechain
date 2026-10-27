---
title: Signing in
sidebar_position: 1
---

import GuideShot from '@site/src/components/GuideShot';

# Signing in

This chapter covers how you get into the console and what you can change about your own
account once you are in. It walks through the sign-in steps, single sign-on, choosing a tenant,
what happens when a session expires, and the user menu.

It is for everyone who uses the console. Your administrator gives you an account; you cannot
create one from the sign-in page.

## The sign-in steps

The sign-in page asks for your email address first, and only then for anything else.

<GuideShot id="signin-identify" alt="The console sign-in page. Below the DeviceChain logo and the line 'Sign in to the management console' is a card with an Email field, a Continue button and a Sign in with SSO button. A language switcher with English and Español sits under the card." />

1. Enter your email address and choose **Continue**.
2. The console checks whether your organization has set up single sign-on for your email
   domain:
   - If it has not, you are asked for your password.
   - If it has, the page says "This address signs in through your organization's single
     sign-on." Choose **Continue with single sign-on** to go to your identity provider. If your
     account has only a password, choose **Use a password instead**.
3. Enter your password and choose **Sign in**.

<GuideShot id="signin-password" alt="The second sign-in step. The Email field now shows the address that was entered, and below it are an empty Password field, a Sign in button, a Sign in with SSO button and a Use a different email link." />

Once you are past the first step, your email address stays on the screen but cannot be edited.
To start again with another address, choose **Use a different email**.

The check in step 2 is about your email **domain**, never about you. It gives the same answer
whether or not an account exists for the address, so it cannot be used to find out who has an
account.

If the email or password is wrong, the console says "Invalid email or password." It does not
say which of the two was wrong. After too many failed attempts it asks you to wait a moment
before trying again.

### Choosing a language

The language switcher under the sign-in card sets the console's language: **English** or
**Español**. Your choice is remembered in this browser and applies straight away. You can change
it again later from the [user menu](#the-user-menu).

## Single sign-on

If your organization signs in through an identity provider, you can choose **Sign in with SSO**
on either step. Enter your email address first: the console needs it to find your
organization's provider. You are sent to the provider, and come back to the console signed in.

If single sign-on is not set up for your address, the console says so and shows the password
field instead.

How single sign-on works, and how an administrator sets it up, is covered in
[Single sign-on](/concepts/single-sign-on) and
[Configuring single sign-on](/guides/configuring-sso).

## Two-factor authentication {#two-factor}

If your account has two-factor authentication, entering the right password is not enough. The console asks for a **verification code** next:

- Enter the six-digit code from your authenticator app. A code works once, so if you have just used one, wait for the next.
- Or choose **Use a recovery code instead** and enter one of the codes you saved when you set it up. It works once.

If the step times out (five minutes, five attempts), you are returned to the password. To turn it on, open the user menu and choose **Account security**; the steps are in [Two-factor authentication](/guides/two-factor-authentication#enrol). If your organization requires it and you have not set it up, the console takes you there after you sign in.

## Choosing a tenant

A tenant is the workspace that holds your organization's devices, rules and dashboards. Your
account can belong to more than one.

- If you belong to **one** tenant, you go straight into it.
- If you belong to **several**, the console shows "Choose a tenant to continue" with one button
  per tenant. Choose the one you want to work in. **Back** returns you to the sign-in form.
- A tenant that requires single sign-on is still listed, marked "Requires single sign-on", but
  you cannot enter it after a password sign-in. Sign in with SSO instead.
- If your account belongs to no tenant yet, the console says so. Ask an administrator to add you
  to one.

Instance operators (superusers) do not choose a tenant at sign-in. They land in the admin
console, which is covered in
[Administering the instance](/user-guide/administering-the-instance).

## When your session expires

A session does not last for ever. If you are away long enough for it to lapse, the console
sends you back to the sign-in page and tells you why.

<GuideShot id="signin-session-expired" alt="The sign-in page with a notice above the card that reads 'Your session expired. Please sign in again.' The Email field is empty." />

Sign in again to carry on. If your tenant started requiring single sign-on while you were
signed in with a password, the notice says so instead, and asks you to use **Sign in with SSO**.

## The user menu {#the-user-menu}

Your name and email address sit at the bottom of the sidebar. Select them to open the user menu.

<GuideShot id="user-menu" alt="The console home page with the user menu open at the bottom left. The menu shows the user's name and email, a three-way theme control with sun, moon and monitor icons, a language control with English selected and Español, and the items Edit profile, Link single sign-on account and Sign out." />

From the user menu you can:

- **Change the theme.** The three icons are **Light**, **Dark** and **System**. System follows
  your computer's setting.
- **Change the language**, as on the sign-in page.
- **Edit your profile** (see below).
- **Link your account to single sign-on.** **Link single sign-on account** connects your
  existing account to your organization's identity provider, so you can sign in through it from
  then on. It needs a recent password sign-in, so it is not offered when you signed in through
  single sign-on. See [linking](/concepts/single-sign-on#linking).
- **Sign out.**

Instance operators also see **Admin console**, and, where the instance offers it, **Metrics**.

### Editing your profile

**Edit profile** opens a panel where you can change the name the console shows for you.

<GuideShot id="user-profile-drawer" alt="The Edit profile panel open on the right of the screen, with the description 'Update your display name.' It shows a greyed-out Email field with the note 'Your sign-in identity; it can't change.', First name and Last name fields, and a Save changes button." />

Change your **First name** and **Last name**, then choose **Save changes**. Your email address
is the identity you sign in with, so it is shown but cannot be changed here.

## Related

- [Single sign-on](/concepts/single-sign-on)
- [Configuring single sign-on](/guides/configuring-sso)
- [Multi-tenancy](/concepts/multi-tenancy)
