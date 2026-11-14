import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { CLI_INSTALL_COMMANDS, CliInstallCommand } from "./cli-install-command";

describe("CliInstallCommand", () => {
  it("offers the Linux installer and guide without a Windows installer", () => {
    render(<CliInstallCommand>{(command) => <code>{command}</code>}</CliInstallCommand>);
    expect(screen.getByText(CLI_INSTALL_COMMANDS.macosLinux)).toBeInTheDocument();
    expect(screen.getByText("Linux x64")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "guideline.md" })).toHaveAttribute("href", "https://10.1.24.90:45671/downloads/selfhost/guideline.md");
    expect(screen.queryByRole("tab", { name: "Windows" })).toBeNull();
  });
});
