with open("server.py", "r") as f:
    content = f.read()

content = content.replace(r"^[a-zA-Z0-9_\-\./]+$", r"^[a-zA-Z0-9_\-./]+$")

with open("server.py", "w") as f:
    f.write(content)
