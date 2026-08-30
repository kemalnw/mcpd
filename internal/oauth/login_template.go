package oauth

import (
	"html/template"
	"strings"
)

var loginTemplate = template.Must(template.New("login").Funcs(template.FuncMap{
	"splitScopes": strings.Fields,
}).Parse(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1, viewport-fit=cover">
<meta name="color-scheme" content="light">
<title>Authorize access · mcpd</title>
<style>
:root {
	color-scheme: light;
	--page: #f8f7fd;
	--surface: #ffffff;
	--text: #111427;
	--muted: #666b86;
	--subtle: #858aa1;
	--border: #e7e4ef;
	--border-strong: #d7d2e5;
	--purple: #6f37e8;
	--purple-strong: #5a28db;
	--purple-soft: #f5f0ff;
	--purple-border: #ded0ff;
	--danger: #b42318;
	--danger-bg: #fff4f3;
	--danger-border: #ffd2cd;
	--shadow: 0 28px 80px rgba(50, 38, 86, .12), 0 5px 18px rgba(50, 38, 86, .06);
}
* { box-sizing: border-box; }
html { -webkit-text-size-adjust: 100%; }
body {
	margin: 0;
	min-width: 280px;
	min-height: 100vh;
	min-height: 100dvh;
	display: grid;
	place-items: center;
	padding: max(24px, env(safe-area-inset-top)) max(16px, env(safe-area-inset-right)) max(24px, env(safe-area-inset-bottom)) max(16px, env(safe-area-inset-left));
	background:
		radial-gradient(circle at 50% 18%, rgba(132, 89, 244, .055), transparent 35%),
		linear-gradient(180deg, #fbfaff 0%, var(--page) 100%);
	color: var(--text);
	font: 15px/1.45 ui-sans-serif, -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, Helvetica, Arial, sans-serif;
	overflow-x: hidden;
}
body::before,
body::after {
	content: "";
	position: fixed;
	z-index: 0;
	width: 430px;
	height: 430px;
	border: 1px solid rgba(111, 55, 232, .10);
	border-radius: 50%;
	pointer-events: none;
}
body::before {
	left: -310px;
	top: 42%;
	box-shadow: 58px 0 0 -1px rgba(248,247,253,1), 58px 0 0 0 rgba(111,55,232,.08), 116px 0 0 -1px rgba(248,247,253,1), 116px 0 0 0 rgba(111,55,232,.06);
}
body::after {
	right: -318px;
	top: 26%;
	box-shadow: -58px 0 0 -1px rgba(248,247,253,1), -58px 0 0 0 rgba(111,55,232,.08), -116px 0 0 -1px rgba(248,247,253,1), -116px 0 0 0 rgba(111,55,232,.06);
}
main {
	position: relative;
	z-index: 1;
	width: min(100%, 590px);
}
.auth-card {
	background: rgba(255,255,255,.97);
	border: 1px solid var(--border);
	border-radius: 21px;
	box-shadow: var(--shadow);
	overflow: hidden;
}
.content { padding: 34px 46px 30px; }
.brand {
	display: flex;
	flex-direction: column;
	align-items: center;
	gap: 7px;
	margin-bottom: 22px;
}
.brand-mark {
	display: block;
	width: 66px;
	height: 66px;
	object-fit: contain;
}
.brand-name {
	font-size: 29px;
	font-weight: 760;
	letter-spacing: -.045em;
	line-height: 1;
}
h1 {
	margin: 0;
	text-align: center;
	font-size: clamp(27px, 5vw, 32px);
	line-height: 1.15;
	letter-spacing: -.035em;
}
.lead {
	max-width: 430px;
	margin: 9px auto 0;
	color: var(--muted);
	font-size: 13.5px;
	text-align: center;
}
.warning {
	display: grid;
	grid-template-columns: 34px minmax(0, 1fr);
	gap: 13px;
	align-items: start;
	margin: 23px 0 4px;
	padding: 13px 15px;
	border: 1px solid var(--purple-border);
	border-radius: 9px;
	background: linear-gradient(135deg, #fbf9ff 0%, var(--purple-soft) 100%);
	color: #36324b;
	font-size: 12.5px;
}
.warning-icon,
.row-icon {
	display: grid;
	place-items: center;
	flex: 0 0 auto;
}
.warning-icon {
	width: 32px;
	height: 32px;
	color: var(--purple);
}
.warning svg,
.row-icon svg,
.input-eye svg,
.submit-icon svg { width: 21px; height: 21px; }
.details {
	margin: 0;
	padding: 0;
}
.detail {
	display: grid;
	grid-template-columns: 42px minmax(0, 1fr);
	gap: 13px;
	align-items: center;
	padding: 12px 0;
	border-bottom: 1px solid var(--border);
}
.row-icon {
	width: 38px;
	height: 38px;
	border-radius: 8px;
	background: #f6f5fa;
	color: #525a77;
}
dt {
	margin: 0 0 2px;
	color: var(--muted);
	font-size: 11.5px;
	font-weight: 560;
}
dd {
	min-width: 0;
	margin: 0;
	color: var(--text);
	font-size: 13.5px;
	font-weight: 650;
	overflow-wrap: anywhere;
}
.resource { color: var(--purple-strong); font-weight: 650; }
.scope-list {
	display: flex;
	flex-wrap: wrap;
	gap: 6px;
	padding-top: 3px;
}
.scope-chip {
	display: inline-flex;
	align-items: center;
	min-height: 24px;
	padding: 3px 9px;
	border: 1px solid #e2d7ff;
	border-radius: 7px;
	background: #f5f0ff;
	color: #5c32c9;
	font-family: ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, "Liberation Mono", monospace;
	font-size: 10.5px;
	font-weight: 620;
}
.form-section {
	padding-top: 13px;
}
.password-heading {
	display: grid;
	grid-template-columns: 42px minmax(0, 1fr);
	gap: 13px;
	align-items: center;
	margin-bottom: 9px;
}
.field-title {
	font-size: 12.5px;
	font-weight: 680;
}
.field-help {
	margin-top: 2px;
	color: var(--subtle);
	font-size: 10.5px;
}
.input-wrap { position: relative; }
input {
	display: block;
	width: 100%;
	min-height: 44px;
	padding: 10px 44px 10px 13px;
	border: 1px solid var(--border-strong);
	border-radius: 7px;
	outline: 0;
	background: #fff;
	color: var(--text);
	font: inherit;
	font-size: 13px;
	box-shadow: 0 1px 2px rgba(17, 20, 39, .02);
	transition: border-color .15s ease, box-shadow .15s ease;
}
input::placeholder { color: #a3a7b7; }
input:hover { border-color: #bbb4ce; }
input:focus-visible {
	border-color: #8b5cf6;
	box-shadow: 0 0 0 3px rgba(111, 55, 232, .12);
}
input[aria-invalid="true"] { border-color: var(--danger); }
.input-eye {
	position: absolute;
	top: 50%;
	right: 13px;
	transform: translateY(-50%);
	display: grid;
	place-items: center;
	color: #67708d;
	pointer-events: none;
}
.error {
	margin: 8px 0 0;
	padding: 9px 11px;
	border: 1px solid var(--danger-border);
	border-radius: 7px;
	background: var(--danger-bg);
	color: var(--danger);
	font-size: 11.5px;
	font-weight: 650;
}
.primary {
	display: flex;
	align-items: center;
	justify-content: center;
	gap: 9px;
	width: 100%;
	min-height: 47px;
	margin-top: 15px;
	padding: 10px 16px;
	border: 0;
	border-radius: 6px;
	background: linear-gradient(100deg, #7435ed 0%, #672ddf 52%, #5a26d7 100%);
	box-shadow: 0 8px 18px rgba(101, 44, 224, .18);
	color: #fff;
	cursor: pointer;
	font: inherit;
	font-size: 13.5px;
	font-weight: 720;
	transition: transform .05s ease, filter .15s ease, box-shadow .15s ease;
}
.primary:hover { filter: brightness(1.035); box-shadow: 0 10px 22px rgba(101, 44, 224, .22); }
.primary:active { transform: translateY(1px); }
.primary:focus-visible,
.cancel:focus-visible {
	outline: 3px solid rgba(111, 55, 232, .24);
	outline-offset: 3px;
}
.submit-icon { display: grid; place-items: center; }
.cancel {
	display: block;
	width: max-content;
	margin: 11px auto 0;
	padding: 4px 8px;
	border: 0;
	background: transparent;
	color: var(--purple-strong);
	cursor: pointer;
	font: inherit;
	font-size: 11.5px;
	font-weight: 580;
}
.consent-note {
	max-width: 390px;
	margin: 15px auto 0;
	color: #8a8ea2;
	font-size: 9.5px;
	line-height: 1.45;
	text-align: center;
}
@media (max-width: 620px) {
	body { place-items: start center; padding-top: max(18px, env(safe-area-inset-top)); }
	main { width: min(100%, 390px); }
	.auth-card { border-radius: 15px; }
	.content { padding: 25px 20px 21px; }
	.brand { gap: 4px; margin-bottom: 18px; }
	.brand-mark { width: 52px; height: 52px; }
	.brand-name { font-size: 25px; }
	h1 { font-size: 24px; }
	.lead { margin-top: 7px; font-size: 12px; }
	.warning { grid-template-columns: 31px minmax(0,1fr); gap: 10px; margin-top: 17px; padding: 11px 12px; font-size: 11px; }
	.warning-icon { width: 29px; height: 29px; }
	.detail { grid-template-columns: 39px minmax(0,1fr); gap: 10px; padding: 10px 0; }
	.row-icon { width: 35px; height: 35px; }
	dt { font-size: 10.5px; }
	dd { font-size: 12.5px; }
	.scope-chip { min-height: 22px; padding: 2px 7px; font-size: 9.5px; }
	.form-section { padding-top: 10px; }
	.password-heading { grid-template-columns: 39px minmax(0,1fr); gap: 10px; margin-bottom: 7px; }
	.field-title { font-size: 11.5px; }
	.field-help { font-size: 9.5px; }
	input { min-height: 42px; font-size: 12px; }
	.primary { min-height: 44px; margin-top: 12px; font-size: 12.5px; }
	.cancel { margin-top: 8px; font-size: 11px; }
	.consent-note { margin-top: 12px; font-size: 9px; }
}
@media (max-height: 650px) and (min-width: 621px) {
	body { place-items: start center; }
}
@media (prefers-reduced-motion: reduce) {
	input, .primary { transition: none; }
}
</style>
</head>
<body>
<main>
	<section class="auth-card" aria-labelledby="page-title">
		<div class="content">
			<header class="brand" aria-label="mcpd">
				<img class="brand-mark" src="/oauth/assets/mcpd-signal-core.webp" alt="mcpd Signal Core">
				<div class="brand-name">mcpd</div>
			</header>

			<h1 id="page-title">Authorize access</h1>
			<p class="lead">Review the details below and approve access if you trust this client.</p>

			<div class="warning" role="note">
				<span class="warning-icon" aria-hidden="true">
					<svg viewBox="0 0 24 24" fill="none"><path d="M12 3 19 6v5c0 4.7-2.9 8-7 10-4.1-2-7-5.3-7-10V6l7-3Z" stroke="currentColor" stroke-width="1.8"/><path d="m9.2 12 1.8 1.8 3.8-4" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"/></svg>
				</span>
				<div>Only authorize access if you initiated this request and trust the client. This action grants access to your mcpd resources.</div>
			</div>

			<dl class="details">
				<div class="detail">
					<span class="row-icon" aria-hidden="true"><svg viewBox="0 0 24 24" fill="none"><circle cx="12" cy="8" r="3.2" stroke="currentColor" stroke-width="1.7"/><path d="M5.8 19c.5-3.6 2.7-5.5 6.2-5.5s5.7 1.9 6.2 5.5" stroke="currentColor" stroke-width="1.7" stroke-linecap="round"/></svg></span>
					<div><dt>Client</dt><dd>{{.Client}}</dd></div>
				</div>
				<div class="detail">
					<span class="row-icon" aria-hidden="true"><svg viewBox="0 0 24 24" fill="none"><circle cx="12" cy="12" r="8" stroke="currentColor" stroke-width="1.7"/><path d="M4 12h16M12 4c2.2 2.2 3.2 4.8 3.2 8S14.2 17.8 12 20c-2.2-2.2-3.2-4.8-3.2-8S9.8 6.2 12 4Z" stroke="currentColor" stroke-width="1.5"/></svg></span>
					<div><dt>Resource</dt><dd class="resource">{{.Resource}}</dd></div>
				</div>
				<div class="detail">
					<span class="row-icon" aria-hidden="true"><svg viewBox="0 0 24 24" fill="none"><path d="M14.6 7.1a4.1 4.1 0 0 0-5.7 5.7L4.5 17.2a1.8 1.8 0 0 0 2.5 2.5l4.4-4.4a4.1 4.1 0 0 0 5.7-5.7l-2.6 2.6-2.7-2.7 2.8-2.4Z" stroke="currentColor" stroke-width="1.6" stroke-linejoin="round"/></svg></span>
					<div>
						<dt>Requested permissions</dt>
						<dd class="scope-list">{{range splitScopes .Scope}}<span class="scope-chip">{{.}}</span>{{end}}</dd>
					</div>
				</div>
			</dl>

			<form class="form-section" method="post" action="/oauth/authorize">
				<input type="hidden" name="transaction" value="{{.Transaction}}">
				<div class="password-heading">
					<span class="row-icon" aria-hidden="true"><svg viewBox="0 0 24 24" fill="none"><rect x="6" y="10" width="12" height="10" rx="2" stroke="currentColor" stroke-width="1.7"/><path d="M9 10V7a3 3 0 0 1 6 0v3" stroke="currentColor" stroke-width="1.7"/></svg></span>
					<div><div class="field-title">Owner password</div><div class="field-help">Enter your mcpd owner password to continue.</div></div>
				</div>
				<div class="input-wrap">
					<label for="owner-password" style="position:absolute;width:1px;height:1px;padding:0;margin:-1px;overflow:hidden;clip:rect(0,0,0,0);white-space:nowrap;border:0">Owner password</label>
					<input id="owner-password" type="password" name="password" placeholder="Enter your owner password" autocomplete="current-password" autocapitalize="none" spellcheck="false" required autofocus {{if .Message}}aria-invalid="true" aria-describedby="password-error"{{end}}>
					<span class="input-eye" aria-hidden="true"><svg viewBox="0 0 24 24" fill="none"><path d="M3.5 12s3.1-5 8.5-5 8.5 5 8.5 5-3.1 5-8.5 5-8.5-5-8.5-5Z" stroke="currentColor" stroke-width="1.6"/><circle cx="12" cy="12" r="2.3" stroke="currentColor" stroke-width="1.6"/></svg></span>
				</div>
				{{if .Message}}<p id="password-error" class="error" role="alert">{{.Message}}</p>{{end}}
				<button class="primary" type="submit" name="decision" value="authorize">
					<span class="submit-icon" aria-hidden="true"><svg viewBox="0 0 24 24" fill="none"><rect x="6" y="10" width="12" height="10" rx="2" stroke="currentColor" stroke-width="1.8"/><path d="M9 10V7a3 3 0 0 1 6 0v3" stroke="currentColor" stroke-width="1.8"/></svg></span>
					Authorize access
				</button>
				<button class="cancel" type="submit" name="decision" value="cancel" formnovalidate>Cancel and go back</button>
			</form>

			<p class="consent-note">By authorizing, you allow {{.Client}} to access the selected resource with the permissions shown above.</p>
		</div>
	</section>
</main>
</body>
</html>`))
