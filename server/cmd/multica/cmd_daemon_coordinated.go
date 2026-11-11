package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/multica-ai/multica/server/internal/daemonupdate"
	"github.com/spf13/cobra"
)

type adoptionPending struct{}

func (adoptionPending) Error() string {
	return "採認待完成：原 runtime 尚需三次新心跳及採認後成功任務回報；使用相同參數再次執行。"
}
func (adoptionPending) ExitCode() int { return 2 }

type legacyPending struct{ phase string }

func (p legacyPending) Error() string {
	if p.phase == "restored_legacy" {
		return "已回舊版，請人工復驗並重新驅動可能漏接的工作。"
	}
	return "新版待成功派工驗收。"
}
func (legacyPending) ExitCode() int { return 2 }

var adoptDaemonCmd = &cobra.Command{
	Use: "coordinated-adopt", Short: "Verify and adopt the current Desktop baseline", Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		if err := requireHumanLocalCommand("daemon coordinated-adopt"); err != nil {
			return err
		}
		manifest, _ := cmd.Flags().GetString("manifest")
		manifestHash, _ := cmd.Flags().GetString("manifest-sha256")
		bundle, _ := cmd.Flags().GetString("bundle")
		entry, _ := cmd.Flags().GetString("cli-entry")
		profile, _ := cmd.Flags().GetString("desktop-profile")
		workdir, _ := cmd.Flags().GetString("work-dir")
		wait, _ := cmd.Flags().GetDuration("wait")
		if wait < 0 || wait > 10*time.Minute {
			return fmt.Errorf("invalid wait duration")
		}
		done, err := daemonupdate.Adopt(cmd.Context(), daemonupdate.AdoptionOptions{ManifestPath: manifest, ManifestSHA256: manifestHash, Bundle: bundle, CLIEntry: entry, Profile: profile, WorkDir: workdir, Wait: wait})
		if err != nil {
			return err
		}
		if !done {
			return adoptionPending{}
		}
		return json.NewEncoder(os.Stdout).Encode(map[string]string{"status": "adopted"})
	},
}

var coordinatedDaemonCmd = &cobra.Command{
	Use:    "coordinated-update",
	Short:  "Resume a verified Desktop update transaction",
	Hidden: true,
	Args:   cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		if err := requireHumanLocalCommand("daemon coordinated-update"); err != nil {
			return err
		}
		journal, _ := cmd.Flags().GetString("journal")
		token, _ := cmd.Flags().GetString("token")
		appPID, _ := cmd.Flags().GetInt("abort-before-handoff-app-pid")
		var done bool
		var err error
		if appPID != 0 {
			done, err = daemonupdate.AbortJournalBeforeHandoff(cmd.Context(), journal, token, appPID)
		} else {
			done, err = daemonupdate.RunJournal(cmd.Context(), journal, token)
		}
		if err != nil {
			return err
		}
		return json.NewEncoder(os.Stdout).Encode(map[string]bool{"complete": done})
	},
}

var legacyPreflightCmd = &cobra.Command{
	Use: "legacy-preflight", Short: "核對舊版 Desktop 導入前的盤點", Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		if err := requireHumanLocalCommand("daemon legacy-preflight"); err != nil {
			return err
		}
		inventory, _ := cmd.Flags().GetString("inventory")
		hash, _ := cmd.Flags().GetString("inventory-sha256")
		manifest, _ := cmd.Flags().GetString("manifest")
		staged, _ := cmd.Flags().GetString("staged-bundle")
		evidence, _ := cmd.Flags().GetString("compatibility-evidence")
		idle, err := daemonupdate.LegacyPreflight(cmd.Context(), daemonupdate.LegacyPreflightOptions{InventoryPath: inventory, InventorySHA256: hash, ManifestPath: manifest, StagedBundle: staged, CompatibilityEvidencePath: evidence})
		if err != nil {
			return err
		}
		return json.NewEncoder(os.Stdout).Encode(map[string]any{"idle": idle, "status": "preflight_only"})
	},
}

var machineFingerprintCmd = &cobra.Command{
	Use: "machine-fingerprint", Short: "顯示本機指紋的 SHA-256", Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		hash, err := daemonupdate.MachineIdentitySHA256()
		if err != nil {
			return err
		}
		return json.NewEncoder(os.Stdout).Encode(map[string]string{"machine_id_sha256": hash})
	},
}

var legacyApplyCmd = &cobra.Command{
	Use: "legacy-apply", Short: "首次導入舊版 Desktop", Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		if err := requireHumanLocalCommand("daemon legacy-apply"); err != nil {
			return err
		}
		journal, _ := cmd.Flags().GetString("journal")
		token, _ := cmd.Flags().GetString("token")
		approvedHash := ""
		var done bool
		var err error
		if journal != "" || token != "" {
			done, err = daemonupdate.RunLegacyJournal(cmd.Context(), journal, token)
		} else {
			inventory, _ := cmd.Flags().GetString("inventory")
			hash, _ := cmd.Flags().GetString("inventory-sha256")
			approvedHash = hash
			manifest, _ := cmd.Flags().GetString("manifest")
			staged, _ := cmd.Flags().GetString("staged-bundle")
			evidence, _ := cmd.Flags().GetString("compatibility-evidence")
			v, readErr := daemonupdate.ReadLegacyInventory(inventory, hash)
			if readErr != nil {
				return readErr
			}
			journal = filepath.Join(v.WorkDir, "journal.json")
			done, err = daemonupdate.BeginLegacy(cmd.Context(), daemonupdate.LegacyPreflightOptions{InventoryPath: inventory, InventorySHA256: hash, ManifestPath: manifest, StagedBundle: staged, CompatibilityEvidencePath: evidence})
		}
		if err != nil {
			if phase, phaseErr := daemonupdate.LegacyJournalPhase(journal, token, approvedHash); phaseErr == nil {
				note := "更新未完成，請依紀錄恢復。"
				if phase == "restored_legacy" {
					note = "已回舊版，請人工復驗並重新驅動可能漏接的工作。"
				}
				if outputErr := json.NewEncoder(os.Stdout).Encode(map[string]string{"status": phase, "journal": journal, "note": note, "reason": err.Error()}); outputErr != nil {
					return outputErr
				}
				if phase == "restored_legacy" {
					return legacyPending{phase: phase}
				}
			}
			return err
		}
		if !done {
			phase, err := daemonupdate.LegacyJournalPhase(journal, token, approvedHash)
			if err != nil {
				return err
			}
			note := "新版待成功派工驗收。"
			if phase == "restored_legacy" {
				note = "已回舊版，請人工復驗並重新驅動可能漏接的工作。"
			}
			if err := json.NewEncoder(os.Stdout).Encode(map[string]string{"status": phase, "journal": journal, "note": note}); err != nil {
				return err
			}
			return legacyPending{phase: phase}
		}
		return json.NewEncoder(os.Stdout).Encode(map[string]string{"status": "complete", "journal": journal, "note": "請確認舊版切換期間可能漏接的工作，必要時重新驅動。"})
	},
}

func init() {
	coordinatedDaemonCmd.Flags().String("journal", "", "Desktop update journal")
	coordinatedDaemonCmd.Flags().String("token", "", "Update transaction token")
	coordinatedDaemonCmd.Flags().Int("abort-before-handoff-app-pid", 0, "Abort only while this original App remains running")
	daemonCmd.AddCommand(coordinatedDaemonCmd)
	adoptDaemonCmd.Flags().String("manifest", "", "Approved fixed release manifest")
	adoptDaemonCmd.Flags().String("manifest-sha256", "", "Independently approved manifest SHA-256")
	adoptDaemonCmd.Flags().String("bundle", "", "Installed Desktop App bundle")
	adoptDaemonCmd.Flags().String("cli-entry", "", "Owner's Desktop CLI entry")
	adoptDaemonCmd.Flags().String("desktop-profile", "", "Existing Desktop daemon profile")
	adoptDaemonCmd.Flags().String("work-dir", "", "Desktop zh-tw-update directory")
	adoptDaemonCmd.Flags().Duration("wait", 2*time.Minute, "Maximum observation time before reporting pending")
	daemonCmd.AddCommand(adoptDaemonCmd)
	legacyPreflightCmd.Flags().String("inventory", "", "已核准的舊版盤點")
	legacyPreflightCmd.Flags().String("inventory-sha256", "", "獨立核准的盤點 SHA-256")
	legacyPreflightCmd.Flags().String("manifest", "", "候選版本資訊")
	legacyPreflightCmd.Flags().String("staged-bundle", "", "已暫存的候選 App")
	legacyPreflightCmd.Flags().String("compatibility-evidence", "", "已核准的舊版資料相容性證據")
	daemonCmd.AddCommand(legacyPreflightCmd)
	daemonCmd.AddCommand(machineFingerprintCmd)
	legacyApplyCmd.Flags().String("inventory", "", "已核准的舊版盤點")
	legacyApplyCmd.Flags().String("inventory-sha256", "", "獨立核准的盤點 SHA-256")
	legacyApplyCmd.Flags().String("manifest", "", "候選版本資訊")
	legacyApplyCmd.Flags().String("staged-bundle", "", "已暫存的候選 App")
	legacyApplyCmd.Flags().String("compatibility-evidence", "", "已核准的舊版資料相容性證據")
	legacyApplyCmd.Flags().String("journal", "", "恢復中的更新紀錄")
	legacyApplyCmd.Flags().String("token", "", "更新紀錄識別碼")
	daemonCmd.AddCommand(legacyApplyCmd)
}
