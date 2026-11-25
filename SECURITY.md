# Security

Please report vulnerabilities privately through [GitHub's security advisories](https://github.com/VOfficial404/colaunch/security/advisories/new), not in public issues.

What Colaunch keeps: refresh tokens of Google accounts and the Hugging Face token in the system's keychain (or, without one, a file only the user can read); settings and the list of runtimes in the app's data folder. Forwarded ports listen on 127.0.0.1 only.
