# Security

Copilot Radar shows data from your agent sessions. This data can contain commands, file paths, and prompts.

## Report a vulnerability

Do not report a vulnerability in a public issue.
Use the private vulnerability report of GitHub: open the **Security** tab of this repository, and then click **Report a vulnerability**.

Give these items in the report:

- The version. Run `copilot-radar -version`.
- The operating system.
- The steps that show the problem.

## Scope

These items are in scope:

- Access to the radar data from a different host or from a web page.
- Data in the `-snapshot` output or in `/api/snapshot` that must not be there.
- The macOS app and the Windows app.

These items are not in scope:

- LAN mode with `-no-auth`.
- Persons who can read your files on the same computer.
