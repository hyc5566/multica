# 彈窗

從 `@multica/ui/components/ui/dialog` 匯入。

## 使用規則

Dialog 用於需要模態介面的獨立操作。上下文選項使用 Popover；重要操作確認使用 AlertDialog。

組合使用 `Dialog`、`DialogTrigger`、`DialogContent`、`DialogHeader`、`DialogTitle`、`DialogDescription`、`DialogFooter` 和 `DialogClose`。Dialog 沒有 `variant` 屬性；表單和長內容只是同一組件的不同組合。

## 行為

提供標題和簡短說明。由組件處理焦點限制、Escape、點擊外部關閉和焦點恢復。使用 `DialogTrigger`，讓焦點能返回開啟按鈕。表單欄位需要標籤。長內容和窄屏下，底部操作必須可訪問。

提交成功後再關閉；失敗時保留輸入並顯示錯誤。UI Lab 中的儲存是本地演示，不傳送 API 請求。

## 正確示例

```tsx
<Dialog>
  <DialogTrigger render={<Button />}>編輯標題</DialogTrigger>
  <DialogContent>
    <DialogHeader>
      <DialogTitle>編輯標題</DialogTitle>
      <DialogDescription>更新任務標題。</DialogDescription>
    </DialogHeader>
    <label>標題<Input defaultValue="檢查介面" /></label>
    <DialogFooter>
      <DialogClose render={<Button variant="outline" />}>取消</DialogClose>
      <Button onClick={saveThenClose}>儲存</Button>
    </DialogFooter>
  </DialogContent>
</Dialog>
```

## 避免

```tsx
<div role="dialog" className="fixed inset-0">
  <Input placeholder="標題" />
  <Button onClick={() => { save(); close(); }}>儲存</Button>
</div>
```

僅設定 role 不會提供無障礙名稱、焦點管理和關閉行為。異步儲存成功前不要關閉彈窗。

## 動效

彈窗和遮罩讀取 `packages/ui/styles/tokens.css` 中的 `--dialog-enter-duration`、`--dialog-exit-duration`、`--dialog-enter-easing` 和 `--dialog-exit-easing`。預設保持現有的 100ms/ease 動畫，彈窗同時淡入淡出並在 95% 和 100% 之間縮放。

參數影響網頁端和桌面端的共享 Dialog，以及使用 DialogContent 的組合；調用方自行覆蓋動畫時除外。不影響 Popover、Tooltip、AlertDialog 和 JavaScript 動效常量。

遵循系統減少動態效果設定，禁用彈窗和遮罩動畫。UI Lab 的播放速度僅用於預覽，不參與匯出。開啟、關閉和重播真實組件，也要快速交替開啟與關閉，檢查中斷效果。

## 應用修改

檢查兩種主題和鍵盤操作。儲存方案，或匯出 CSS 併合併到 `packages/ui/styles/tokens.css` 的 `:root` 區塊。匯出不會直接修改產品程式碼。
