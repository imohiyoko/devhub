import json

SECRET_KEYS = {'password', 'secret', 'apiKey', 'api_key', 'api-key', 'token'}

def is_secret_key(k):
    lower = k.lower()
    return any(x in lower for x in SECRET_KEYS)

def sanitize_db_connection(profile):
    if not isinstance(profile, dict):
        return profile
    return {k: v for k, v in profile.items() if not is_secret_key(k)}

def sanitize_settings(settings):
    sanitized = dict(settings)
    if isinstance(sanitized.get('db_connections'), list):
        sanitized['db_connections'] = [
            sanitize_db_connection(profile)
            for profile in sanitized['db_connections']
            if isinstance(profile, dict)
        ]
    return sanitized
