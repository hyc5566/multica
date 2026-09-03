"use client";

import { useState } from "react";
import { Activity } from "lucide-react";
import {
  ISSUE_EXECUTION_STATES,
  ISSUE_EXECUTION_STATE_KEY,
  type IssueExecutionState,
  useSetIssueMetadata,
} from "@multica/core/issues";
import { PropertyPicker, PickerItem } from "./property-picker";
import { useT } from "../../../i18n";

export function ExecutionStatePicker({
  issueId,
  state,
}: {
  issueId: string;
  state: IssueExecutionState;
}) {
  const [open, setOpen] = useState(false);
  const { t } = useT("issues");
  const update = useSetIssueMetadata();

  return (
    <PropertyPicker
      open={open}
      onOpenChange={setOpen}
      width="w-44"
      align="start"
      trigger={
        <>
          <Activity className="size-3.5 shrink-0 text-muted-foreground" />
          <span className="truncate">{t(($) => $.execution_state[state])}</span>
        </>
      }
    >
      {ISSUE_EXECUTION_STATES.map((value) => (
        <PickerItem
          key={value}
          selected={value === state}
          onClick={() => {
            update.mutate({ issueId, key: ISSUE_EXECUTION_STATE_KEY, value });
            setOpen(false);
          }}
        >
          {t(($) => $.execution_state[value])}
        </PickerItem>
      ))}
    </PropertyPicker>
  );
}
