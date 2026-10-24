export default {
  codex: {
    timezone: {
      label: '请求时区',
      hint: '用于 OpenAI 请求 Date 头与时间相关指纹；保持默认即跟随 {timezone}。',
      placeholder: '选择请求时区',
      followDefault: '跟随默认',
      loadFailed: '请求时区列表加载失败，仍可按默认时区保存账号。'
    },
    ticket: {
      title: 'Codex 票据中心 · {name}',
      loadFailed: '票据数据加载失败',
      openCenterHint: '点击打开票据中心',
      expiresAt: '过期时间 {time}',
      reasonLabel: '原因：{reason}',
      moreModels: '另有 {count} 个模型',
      state: {
        ready: '就绪',
        expiring: '临期',
        expired: '过期',
        missing: '缺失',
        blocked: '阻塞',
        paused: '限流暂停',
        disabled: '未启用打票',
        not_participating: '未参与',
        unavailable: '不可用'
      },
      summary: {
        allReady: '全部模型就绪',
        blocked: '存在阻塞模型',
        missing: '票据缺失/过期',
        disabled: '打票未启用',
        notParticipating: '账号未参与打票',
        partial: '部分就绪',
        unavailable: '不适用'
      },
      reason: {
        ready: '就绪',
        rate_limited: '限流退避中',
        participation_disabled: '该模型已关闭参与',
        disabled: '全局打票已关闭',
        ticket_unavailable: '票据不可用'
      },
      global: {
        enabled: '全局打票已启用',
        disabled: '全局打票已关闭 —— 手动打票不可用',
        allowWithoutTicketOn: '无票请求放行',
        allowWithoutTicketOff: '无票请求被拦截',
        poolAll: '代理池：全部可用代理',
        poolCustom: '代理池：已选 {count} 个代理',
        availableProxies: '{count} 个代理可参与打票',
        cadence: '重试 {min}-{max}s · 主动刷新 {refresh}s',
        loadFailed: '全局打票设置加载失败'
      },
      participation: {
        title: '账号 / 模型参与开关',
        description: '控制该账号（以及每个模型）是否参与打票。',
        accountEnabled: '账号参与打票'
      },
      tabs: {
        current: '当前票据',
        history: '打票流水',
        events: '事件时间线',
        invalidations: '失效历史'
      },
      current: {
        model: '模型',
        harvest: '手动打票',
        harvesting: '打票中...',
        harvestResult: '本次打票结束：{outcome} · {model} · {duration} ms',
        noTicket: '该账号没有可用的票据状态',
        ticketState: '票据状态',
        length: '票据长度',
        lengthMismatch: '（不符合预期）',
        capturedAt: '捕获时间',
        expiresAt: '过期时间',
        remaining: '剩余时长',
        reason: '原因',
        quotaResetAt: '配额重置时间',
        components: '组成部分',
        turnState: 'turn-state 存在',
        cookie: '路由 Cookie 存在',
        harvestEnabled: '打票已启用',
        harvestPaused: '打票已暂停',
        blocked: '被门控阻塞'
      },
      manualUnavailable: {
        disabled: '全局打票已关闭，手动打票不可用',
        no_proxy: '没有可用打票代理，手动打票不可用',
        unavailable: '当前无法手动打票'
      },
      columns: {
        time: '时间',
        model: '模型',
        outcome: '结果',
        trigger: '触发方式',
        httpStatus: 'HTTP',
        ticketLength: '票据长度',
        duration: '耗时',
        proxy: '代理',
        reason: '原因',
        kind: '类型',
        detail: '详情',
        route: '路由',
        invalidatedCurrent: '是否失效当前票',
        generation: '票据代次'
      },
      history: {
        total: '共 {total} 条流水',
        filterAll: '全部流水',
        filterSuccess: '仅成功'
      },
      events: {
        total: '共 {total} 个事件',
        filterAll: '全部事件',
        filterAttempts: '打票尝试',
        filterSuccess: '成功尝试',
        filterFailure: '失败尝试',
        filterInvalidation: '失效事件',
        invalidatedCurrent: '当前票据被失效'
      },
      invalidations: {
        total: '共 {total} 条失效记录',
        detailTitle: '失效详情',
        presence: '票据存在性',
        presenceValue: '原始票据：{originalTicket} · 原始 Cookie：{originalCookie} · 返回 Cookie：{returnedCookie} · 返回 Set-Cookie 数：{setCookies}'
      }
    },
    ticketSettings: {
      title: 'Codex 打票设置',
      description: 'x-codex-turn-state 打票器的全局开关、重试节奏与代理池。',
      loadFailed: '打票设置加载失败',
      saveFailed: '打票设置保存失败',
      enabled: {
        title: '启用打票',
        description: '关闭后不再主动打票，被门控的模型回落到「无票放行」策略。'
      },
      allowWithoutTicket: {
        title: '无票是否放行',
        description: '开启后，即使没有就绪票据，被门控模型的请求仍会转发。'
      },
      cadence: {
        title: '重试与刷新节奏',
        description: '失败退避区间，以及就绪票据的主动刷新周期。',
        retryMin: '重试最小间隔（秒）',
        retryMax: '重试最大间隔（秒）',
        refresh: '主动刷新周期（秒）',
        errors: {
          retryMinPositive: '重试最小间隔必须不小于 1 秒',
          retryMaxPositive: '重试最大间隔必须不小于 1 秒',
          refreshPositive: '主动刷新周期必须不小于 1 秒',
          retryMinGreaterThanMax: '重试最小间隔不能大于最大间隔'
        }
      },
      pool: {
        title: '打票代理池',
        description: '复用「IP 设置」中的代理记录；自定义模式只轮换选中的代理。',
        modeAll: '全部可用代理',
        modeCustom: '自定义代理列表',
        available: '当前可用代理 {count} 个',
        empty: '没有代理，请先在 IP 设置中创建代理。',
        errors: {
          noProxySelected: '自定义模式至少需要选择一个代理'
        }
      },
      configOnly: {
        title: '仅配置文件可调参数',
        description: '以下配置键从后端配置文件读取，管理端接口不提供写入能力：'
      }
    },
    degradation: {
      title: 'Codex 降智检测 · {name}',
      runFailed: '降智检测失败',
      status: {
        normal: '正常',
        degraded: '降智',
        uncertain: '不确定',
        failed: '失败'
      },
      models: {
        title: '待检测模型',
        description: '每个选中的模型都会用账号自身凭据执行一次 ModelTrace 原生探测。',
        bank: '指纹库：{bank}',
        selected: '已选 {count} 个',
        empty: '没有可用的检测模型',
        required: '请至少选择一个模型'
      },
      run: '开始检测',
      running: '检测中...',
      runningHint: '正在用账号凭据探测 {count} 个模型，已耗时 {elapsed} 秒（单模型最长 120 秒）。',
      result: {
        title: '检测结果',
        checkedAt: '检测时间 {time}',
        reason: '整体原因：{reason}',
        pending: '等待中',
        predictedModel: '实际预测模型',
        probability: '概率',
        parsedCount: '解析数字数',
        httpStatus: 'HTTP',
        reasonLabel: '原因'
      },
      history: {
        title: '最近探测结果',
        total: '共 {total} 条记录',
        refresh: '刷新历史',
        time: '时间',
        model: '模型',
        status: '状态',
        predictedModel: '实际预测模型',
        probability: '概率',
        source: '来源'
      }
    },
    degradationSettings: {
      title: '自动降智检测',
      description: '按周期用 ModelTrace 指纹探测 OpenAI 账号，并对降智账号临时停调。',
      loadFailed: '降智检测设置加载失败',
      saveFailed: '降智检测设置保存失败',
      interval: '检测间隔（分钟）',
      block: '降智后封锁时长（分钟）',
      enabled: {
        title: '启用自动检测',
        description: '关闭后后台运行器不会产生任何上游探测。'
      },
      models: {
        title: '探测模型',
        description: '只能选择内嵌指纹库中的模型。',
        bank: '指纹库：{bank}',
        empty: '没有可用的检测模型'
      },
      errors: {
        intervalPositive: '检测间隔必须不小于 1 分钟',
        blockPositive: '封锁时长必须不小于 1 分钟',
        modelsRequired: '请至少选择一个探测模型',
        modelsNotAvailable: '所选模型不在当前指纹库中'
      },
      runNow: {
        action: '立即运行一轮',
        running: '触发中...',
        hint: '立即触发一轮自动检测；功能关闭时不会产生探测。',
        triggered: '已触发一轮检测',
        failed: '触发检测失败'
      },
      template: {
        title: '探测模板（JSONL）',
        description: '保存前会校验模板；非法 JSONL 会被拒绝。内容为空时回退到内嵌默认模板。',
        placeholder: '在此粘贴 JSONL 探测模板',
        restoreDefault: '恢复默认',
        hint: '恢复默认会清除已保存的覆盖内容，改用内嵌默认模板。',
        required: '模板内容不能为空',
        invalid: '模板非法，未保存',
        saved: '探测模板已保存',
        restored: '已恢复默认探测模板'
      },
      configOnly: {
        title: '仅配置文件可调参数',
        description: '以下配置键从后端配置文件读取，管理端接口不提供写入能力：'
      }
    }
  }
}
