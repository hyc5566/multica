import type { LandingDict } from "./types";
import { zhDemoCopy } from "./demo-zh";

export const zhSiteCopy: Pick<LandingDict, "site" | "home" | "demo" | "meta"> = {
  demo: zhDemoCopy,
  meta: {
    home: {
      title: "Multica — 人和 AI 代理 團隊的專案管理平台",
      description: "原始碼公開的平台，把程式開發 AI 代理 變成真正的隊友。分派任務、跟進進度、累積技能。",
      ogTitle: "Multica — 人和 AI 代理 團隊的專案管理平台",
      ogDescription: "在一個地方管理你的人和 AI 代理 團隊。",
    },
    about: {
      title: "關於我們",
      description: "瞭解 Multica（Multiplexed Information and Computing Agent）：一個原始碼公開、為人和 AI 代理 團隊打造的專案管理平台。",
      ogTitle: "關於 Multica",
      ogDescription: "Multica 背後的故事，以及我們為什麼要為人和 AI 代理 團隊做專案管理。",
    },
    changelog: {
      title: "更新日誌",
      description: "看看 Multica 有什麼新變化：最新功能、改進和修復。",
      ogTitle: "更新日誌 | Multica",
      ogDescription: "Multica 的最新更新和版本發布。",
    },
    contactSales: {
      title: "聯絡商務",
      description: "和 Multica 團隊聊聊，怎麼在你的公司落地人和 AI 代理 協作的工作流程。",
      ogTitle: "聯絡商務 — Multica",
      ogDescription: "介紹一下你的團隊，我們會在三個工作日內回覆。",
    },
    download: {
      title: "下載 Multica",
      description: "下載適用於 macOS、Windows 或 Linux 的 Multica，或在伺服器和遠端開發機上安裝 CLI。",
      ogTitle: "下載 Multica",
      ogDescription: "取得自帶守護程序的 Multica 桌面應用，或在伺服器和遠端開發機上安裝 CLI。",
    },
    licensing: {
      title: "授權協議",
      description: "什麼時候可以免費使用 Multica，什麼時候需要商業授權，用常見場景講清楚。",
      ogTitle: "授權協議 — Multica",
      ogDescription: "在組織內部自行架設免費。把 Multica 作為代管服務提供給他人，需要商業授權。",
    },
    privacy: {
      title: "隱私政策",
      description: "Multica 如何在 multica.ai 和 Multica Cloud 上收集、使用和保護個人資訊。",
      ogTitle: "隱私政策 — Multica",
      ogDescription: "Multica 如何收集、使用和保護個人資訊。",
    },
  },
  site: {
    nav: {
      product: "產品",
      solutions: "解決方案",
      resources: "資源",
      customers: "客戶",
      pricing: "價格",
      documentation: "文件",
      changelog: "更新日誌",
      useCases: "案例",
      blog: "部落格",
      download: "下載",
      company: "公司",
      about: "關於我們",
      careers: "加入我們",
      press: "媒體與品牌",
      contactSales: "聯絡商務",
      trust: "信任與合規",
      security: "安全",
      licensing: "授權協議",
      privacy: "隱私政策",
    },
    header: {
      home: "Multica 首頁",
      githubStars: "GitHub，{count} 顆星",
      logIn: "登入",
      getStarted: "開始使用",
      dashboard: "進入工作台",
    },
    footer: {
      tagline: "人和 AI 代理 一起工作的團隊空間。",
      navigation: "頁腳導航",
      language: "語言",
      copyright: "© {year} Multica. 保留所有權利。",
      starTurns: "大家已經把這顆星吹轉了 {count} 圈",
    },
    localeSuggestion: {
      message: "Multica 提供中文版。",
      action: "切換到中文",
      dismiss: "關閉",
    },
    cta: {
      title: "今天就讓第一個 AI 代理 開工",
      lead: "連接你已經在用的 AI 代理，給它派第一個 Issue。",
      getStarted: "開始使用",
      bookDemo: "預約展示",
    },
  },
  home: {
    hero: {
      headlinePrefix: "讓 ",
      headlineSuffix: " 成為你的隊友。",
      lead: "你的團隊和 AI 代理 共用一個工作空間：分派任務、跟進進度、審核結果，主動權始終在你手裡。",
      download: "下載",
      downloadFor: "下載 {os} 版",
      alsoFor: "其他版本：{platforms}",
      bookDemo: "預約展示",
      workflowGroup: "工作流程",
      workflows: {
        ship: {
          label: "交付一個功能",
          short: "交付",
          description:
            "一個由工作流程驅動的 Multica 專案看板：Issue 依次經過方案、開發、程式碼審查、審批和已上線，輪流交給規劃 AI 代理、程式開發 AI 代理、審查 AI 代理 和一位同事",
        },
        launch: {
          label: "發布內容",
          short: "發布",
          description:
            "一個由工作流程驅動的市場團隊專案看板：一篇發布文章依次經過需求、研究、撰寫、編輯和已發布，輪流交給研究 AI 代理、寫作 AI 代理 和一位編輯",
        },
        triage: {
          label: "處理使用者回饋",
          short: "回饋",
          description:
            "一個由工作流程驅動的支援團隊專案看板：Autopilot 把使用者回饋歸入待處理，Issue 再依次經過重現、修復、回覆和已解決，輪流交給分診 AI 代理、QA AI 代理 和程式開發 AI 代理",
        },
      },
    },
    logoWall: { title: "這些公司和高校的人在用 Multica" },
    features: {
      title: "為人和 AI 代理 組成的團隊而生",
      items: {
        runtimes: {
          title: "跑在你的機器上，也能跑在我們的雲上",
          body: "一條命令連接筆記本或伺服器，Multica 會自動找到上面裝好的程式開發 AI 代理。也可以直接在代管的雲沙箱里執行。",
          alt: "執行時頁面：列出兩台機器和 Multica Cloud，以及每台上找到的程式開發工具",
        },
        workflows: {
          title: "工作流程把每一步交接好",
          body: "給每個狀態指定負責人。Issue 進入下一步，就自動交給這一步的 AI 代理 或同事。",
          alt: "“交付一個功能”工作流程：方案交給 Lead Agent，開發交給 Backend Agent，程式碼審查交給 QA Agent，審批交給 Mia Chen，最後到已上線；右側打開了開發這一步的交接設定",
        },
        chat: {
          title: "在聊天里 @ AI 代理",
          body: "在 Slack、飛書、釘釘、企業微信或 Telegram 里都可以。AI 代理 會讀完對話、建好 Issue，然後開工。",
          alt: "團隊聊天頻道：Mia 就匯出失敗的問題 @ 了 Backend Agent，AI 代理 在頻道里回覆，附上它新建並正在處理的 Multica Issue 連結",
        },
        autopilots: {
          title: "Autopilot 按計畫自動執行",
          body: "依賴升級、問題分診、定期報告，每小時或每週一自動建好 Issue，交到 AI 代理 手裡。",
          alt: "Autopilot 列表：五個定時任務，以及各自的 AI 代理 和下次執行時間",
        },
        skills: {
          title: "全隊共享的技能",
          body: "團隊做一件事的方法只要寫一次，每個 AI 代理 都會照著做。",
          alt: "技能列表：團隊寫好的五個技能，以及用到它們的 AI 代理",
        },
      },
    },
    agents: {
      lead: "不用學新的 AI 代理。",
      title: "直接用你已經在用的。",
      all: "全部 27 款 AI 代理",
    },
    updates: {
      eyebrow: "更新日誌",
      title: "最近發布。",
      latest: "最新",
      more: "+{count} 項",
      all: "全部版本",
    },
  },
};
