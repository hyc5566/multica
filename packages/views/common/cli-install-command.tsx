"use client";

import type { ReactNode } from "react";

export const CLI_INSTALL_COMMANDS = {
  macosLinux: "curl -fsSL https://10.1.24.90:45671/downloads/selfhost/install.sh -o install.sh && bash install.sh",
} as const;
export const SELFHOST_GUIDELINE_URL = "https://10.1.24.90:45671/downloads/selfhost/guideline.md";
const PLATFORM = "Linux x64";
const GUIDE = "guideline.md";

export function CliInstallCommand({ children }: {
  children: (command: string) => ReactNode;
}) {
  return (
    <div>
      <p>{PLATFORM}</p>
      {children(CLI_INSTALL_COMMANDS.macosLinux)}
      <a className="text-caption text-link underline" href={SELFHOST_GUIDELINE_URL}>{GUIDE}</a>
    </div>
  );
}
