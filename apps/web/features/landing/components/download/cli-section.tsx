"use client";

import { useState } from "react";
import { Check, Copy } from "@multica/ui/icons";
import { copyText } from "@multica/ui/lib/clipboard";
import { cn } from "@multica/ui/lib/utils";
import { useLocale } from "../../i18n";
import { Container, Eyebrow, focusRing } from "../../site/primitives";

const SETUP_CMD = "multica setup";

/** The Taiwan installer supports macOS and Linux. */
export function CliSection({ installerUrl }: { installerUrl?: string }) {
  const { t } = useLocale();
  const d = t.download.cli;

  return (
    <section id="cli" className="border-t border-site-rule py-site-section">
      <Container className="grid gap-8 lg:grid-cols-[5fr_7fr] lg:items-center lg:gap-16">
        <div>
          <Eyebrow>CLI</Eyebrow>
          <h2 className="mt-3 font-site-display text-site-title text-site-ink text-balance">{d.title}</h2>
          <p className="mt-3 max-w-[30rem] text-site-body text-site-muted text-pretty">{d.sub}</p>
        </div>
        <div className="min-w-0 rounded-site-panel bg-site-inverse p-5 text-site-inverse-ink">
          <p className="font-site-mono text-[0.75rem] text-site-inverse-muted">{d.platformMacosLinux}</p>
          {installerUrl && /^https:\/\/github\.com\/hyc5566\/multica\/releases\/download\/[a-zA-Z0-9._-]+\/install\.sh$/.test(installerUrl) ? (
            <CommandRow label={d.installLabel} cmd={`curl -fsSL '${installerUrl}' -o install.sh && bash install.sh --login`} />
          ) : (
            <p role="status" className="mt-4 text-site-inverse-muted">{t.download.allPlatforms.unavailable}</p>
          )}
          <CommandRow label={d.startLabel} cmd={SETUP_CMD} />
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
