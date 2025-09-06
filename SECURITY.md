# Security Policy

Thank you for taking the time to responsibly disclose security issues for **Deribit Options Hedger**.

**Contact (preferred):** bokim2121@hstreet.finance

---

## Report options (simple, modern)

We provide easy reporting channels. Please choose whichever is most convenient:

### 1) Email (simple)
- Send reports to **bokim2121@hstreet.finance**
- Suggested subject: `Security report — Deribit Options Hedger`
- Include: short summary, affected commit hash or tag, reproduction steps, expected vs actual behavior, and any PoC (if available).
- **Do not** include real secrets (API keys, private keys) directly in email. If you must provide sensitive attachments, request a secure upload link as described below.

### 2) GitHub Private Security Advisory (preferred when available)
- Use GitHub: Security → Advisories for confidential, tracked reports (reporter needs a GitHub account).
- Benefits: private discussion, coordinated disclosure, CVE support.

### 3) Secure file upload (practical)
- If you need to send large or sensitive files (logs, PoC data), email `bokim2121@hstreet.finance` with the subject line `Request upload link`.

---

## What to include (checklist)
- Title and short summary  
- Affected version(s) or commit hash/tag  
- Minimal reproducible steps or PoC code (if possible)  
- Expected result vs actual result  
- Any relevant logs, stack traces, or screenshots  
- Contact email for follow-up

**Avoid posting secrets in public issues or discussions.** Use the secure upload process for sensitive artifacts.

---

## Response & handling timeline (target)
- **Acknowledgement:** within 48 hours  
- **Initial triage & classification:** within 72 hours  
- **Fix / mitigation plan:** within 14 days for high/critical issues where feasible  
- **Coordinated disclosure / public advisory:** coordinated with the reporter; typically within 30–90 days depending on complexity

If you report an active exploit or ongoing attack, please mark the report `CRITICAL` and indicate urgency in the subject line.

---

## Severity guide (informational)
- **Critical:** Remote code execution, data exfiltration, or major financial/operational impact.  
- **High:** Authentication bypass, privilege escalation, major data leak.  
- **Medium:** Information disclosure with limited scope, logic bugs causing incorrect behavior.  
- **Low:** Minor issues, documentation problems, edge-case errors.

If you can provide a CVSS vector, include it — we will use it to help triage.

---

## Safe testing guidelines
When researching or reproducing issues, please avoid actions that could harm real users or third parties:

- Do not perform DDoS or other disruptive attacks on production systems.  
- Do not access or modify data belonging to other users.  
- Do not attempt to social-engineer staff or third parties for access.  
- Prefer testing in a local fork, a sandbox, or authorized test accounts.  
- If you need to test against production, request prior written consent.

---

## Anonymity and credit
- You may request anonymity; we will respect that when publishing advisories.  
- We typically credit reporters (name or handle) unless you ask to remain anonymous.

---

## Supported versions
- `main` — active development (priority for fixes)  
- `v1.x` — stable; security fixes considered when feasible

Please update this section to match your official support policy.

---

## Legal safe harbor
We welcome good-faith security research. If you follow this policy and act in good faith to avoid privacy/availability harm, we will not pursue legal action. This is not a legal warranty; consult legal counsel for formal agreements.

---

## Contact
- Primary: **bokim2121@hstreet.finance**  
- For upload link requests: send an email with subject `Request upload link`

---

## Revision history
- 2025-09-05: Initial SECURITY.md (contact and simple reporting options)

---

Thank you — your reports help keep the project and its users safe.
