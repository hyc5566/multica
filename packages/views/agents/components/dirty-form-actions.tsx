"use client";

import { Loader2, RotateCcw, Save } from "lucide-react";
import { Button } from "@multica/ui/components/ui/button";
import { useT } from "../../i18n";

export function DirtyFormActions({
  dirty,
  saving,
  saveDisabled = false,
  onReset,
  onSave,
  className = "",
}: {
  dirty: boolean;
  saving: boolean;
  saveDisabled?: boolean;
  onReset: () => void;
  onSave: () => void;
  className?: string;
}) {
  const { t } = useT("agents");
  if (!dirty) return null;

  return (
    <div className={`flex flex-wrap items-center justify-end gap-2 ${className}`}>
      <span role="status" className="mr-1 text-caption text-muted-foreground">
        {t(($) => $.tab_body.common.unsaved_changes)}
      </span>
      <Button
        type="button"
        variant="outline"
        size="sm"
        onClick={onReset}
        disabled={saving}
      >
        <RotateCcw className="size-3.5" aria-hidden="true" />
        {t(($) => $.tab_body.common.reset)}
      </Button>
      <Button
        type="button"
        size="sm"
        onClick={onSave}
        disabled={saving || saveDisabled}
      >
        {saving ? (
          <Loader2
            className="size-3.5 animate-spin motion-reduce:animate-none"
            aria-hidden="true"
          />
        ) : (
          <Save className="size-3.5" aria-hidden="true" />
        )}
        {t(($) => $.tab_body.common.save_changes)}
      </Button>
    </div>
  );
}
