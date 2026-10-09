# 按鈕

從 `@multica/ui/components/ui/button` 匯入。

## 使用規則

Button 用於執行操作，頁面跳轉使用連結。每個彈窗或表單保留一個突出顯示的主要操作。

| Variant | 用途 |
| --- | --- |
| `default` | 儲存、新建等主要操作 |
| `outline` | 取消等次要操作 |
| `secondary` | 柔和底色的普通操作 |
| `ghost` | 工具欄中弱化顯示的操作 |
| `brand` | 已啟用的篩選器等選中狀態 |
| `brandSubtle` | 有活動但不需要突出選中的狀態 |
| `destructive` | 刪除等破壞性操作 |
| `link` | 連結樣式的操作；頁面跳轉仍使用真實連結 |

## 尺寸與狀態

文字尺寸：`xs`、`sm`、`default`、`lg`。圖示尺寸：`icon-xs`、`icon-sm`、`icon`、`icon-lg`。

操作不可用時使用 `disabled`。提交中組合載入圖示、`disabled` 和 `aria-busy`；Button 沒有 `loading` 屬性。純圖示按鈕必須有無障礙名稱。保留鍵盤焦點樣式。

## 正確示例

```tsx
<Button type="submit" variant="default">儲存</Button>
<Button type="button" variant="outline" onClick={onCancel}>取消</Button>
<Button variant="ghost" size="icon-xs" aria-label="新增條目"><Plus /></Button>
```

## 避免

```tsx
<Button variant="primary" loading>儲存</Button>
<Button className="h-6 bg-blue-500 px-2">儲存</Button>
```

不支援 `primary` 和 `loading`。優先使用現有尺寸和語義樣式，再考慮覆蓋大小或顏色。

## 參數與應用

`--button-height-*`、`--button-padding-*`、`--button-gap-*` 分別控制各尺寸。顏色使用語義 token。尺寸修改影響所有使用該尺寸的按鈕；局部樣式覆蓋可能優先。

UI Lab 匯出 `packages/ui/styles/tokens.css` 的 CSS 補丁。對比淺色、深色下的原始與修改效果，合併到對應區塊，再檢查程式碼差異。在瀏覽器中儲存方案不會修改產品程式碼。
