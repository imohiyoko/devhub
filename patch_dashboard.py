with open("dashboard/index.html", "r") as f:
    content = f.read()

pattern = """  {
    id: 'ports',"""
replacement = """  {
    id: 'git',
    href: '/git',
    icon: '⎇',
    name: 'git',
    desc: 'lazygit風ブラウザUI。ステージング・コミット・ブランチ管理・プッシュ・プルをキーボードで操作',
  },
  {
    id: 'ports',"""

content = content.replace(pattern, replacement)

with open("dashboard/index.html", "w") as f:
    f.write(content)
