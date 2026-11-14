"use client";

import { useState } from "react";
import { Check, Copy } from "@multica/ui/icons";
import { copyText } from "@multica/ui/lib/clipboard";
import { cn } from "@multica/ui/lib/utils";
import { useLocale } from "../../i18n";
import { Container, Eyebrow, focusRing } from "../../site/primitives";
import type { CliAssets } from "../../utils/parse-release-assets";
import { CLI_INSTALL_COMMANDS, SELFHOST_GUIDELINE_URL } from "@multica/views/common/cli-install-command";

const SETUP_CMD = "multica login";

export function CliSection({ assets = {} }: { assets?: CliAssets }) {
  const { t, locale } = useLocale();
  const d = t.download.cli;

  return (
    <section id="cli" className="border-t border-site-rule py-site-section">
      <Container className="grid gap-8 lg:grid-cols-[5fr_7fr] lg:items-center lg:gap-16">
        <div>
          <Eyebrow>CLI</Eyebrow>
          <h2 className="mt-3 font-site-display text-site-title text-site-ink text-balance">{d.title}</h2>
          <p className="mt-3 max-w-[30rem] text-site-body text-site-muted text-pretty">{(locale === "zh-Hans" || locale === "zh-Hant") ? "獨立安裝目前僅支援 Linux x64。使用 App 的機器由 App 管理；Windows 請參考指南。" : "Standalone installation supports Linux x64. The App manages machines with the App installed; see the guide for Windows."}</p>
          <a href={SELFHOST_GUIDELINE_URL} className="mt-4 inline-block text-site-small text-site-ink underline underline-offset-4">guideline.md</a>
          <ul className="mt-4 space-y-2">
            {(Object.entries(assets) as [keyof CliAssets, string][]).map(([platform, href]) => (
              <li key={platform}>
                <a href={href} className={cn("text-site-small text-site-ink underline underline-offset-4", focusRing)}>
                  {t.download.platforms[platform]} · CLI / daemon
                </a>
              </li>
            ))}
          </ul>
        </div>
        <div className="min-w-0 rounded-site-panel bg-site-inverse p-5 text-site-inverse-ink">
          <p className="font-site-mono text-[0.75rem] text-site-inverse-muted">Linux x64</p>
          <CommandRow label={d.installLabel} cmd={CLI_INSTALL_COMMANDS.macosLinux} />
          <CommandRow label={(locale === "zh-Hans" || locale === "zh-Hant") ? "登入" : "Sign in"} cmd={SETUP_CMD} />
        </div>
      </Container>
    </section>
  );
}

function CommandRow({ label, cmd }: { label: string; cmd: string }) {
  const { copyLabel, copiedLabel } = useLocale().t.download.cli;
  const [copied, setCopied] = useState(false);

  const onCopy = async () => {
    if (await copyText(cmd)) {
      setCopied(true);
      setTimeout(() => setCopied(false), 1800);
    }
  };

  return (
    <div className="mt-4">
      <p className="font-site-mono text-[0.75rem] text-site-inverse-muted"># {label}</p>
      <div className="mt-1 flex items-start gap-3 font-site-mono text-[0.8125rem] leading-6">
        <span aria-hidden className="text-site-inverse-muted">
          $
        </span>
        <code className="min-w-0 flex-1 break-all">{cmd}</code>
        <button
          type="button"
          onClick={onCopy}
          aria-label={copied ? copiedLabel : copyLabel}
          className={cn(
            "flex size-6 shrink-0 items-center justify-center rounded-site-inner text-site-inverse-muted transition-colors hover:bg-white/10 hover:text-site-inverse-ink",
            focusRing,
            "focus-visible:outline-site-inverse-ink",
          )}
        >
          {copied ? <Check className="size-3.5" /> : <Copy className="size-3.5" />}
        </button>
      </div>
    </div>
  );
}
