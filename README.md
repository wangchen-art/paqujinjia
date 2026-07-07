# 贵金属报价校准工具

自动获取水贝黄金/白银/铂金/钯金实时价格，通过双数据源校准去除商家差价。

## 数据源

- **基准价**: `huangjinjiage.cn` (panjia2.js, 每分钟刷新)
- **快速价**: `zhoutuoiot.cn` (?ajax=1, 每3秒刷新)

## 使用方法

```bash
# 完整校准（首次运行）
./calib.exe

# 快速获取价格（不校准、不写磁盘）
./calib.exe --quick

# JSON 输出（网站后端调用）
./calib.exe --json
```

## 校准原理

```
offset = 典和佳价 - 黄金价格网基准价
实时基准价 = 典和佳实时价 - offset
```

offset 保存在 adjust.json，仅当连续3分钟偏离阈值0.6时才更新。
