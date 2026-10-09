# Curl fixture

## valid payload

```bash
curl -s -X POST http://localhost/api/things/graphql \
  -H 'Content-Type: application/json' \
  -d '{"query":"mutation($r:ThingInput!){createThing(request:$r){token}}",
       "variables":{"r":{"token":"a","name":"A"}}}'
```

## bad variable field

```bash
curl -s -X POST http://localhost/api/things/graphql \
  -d '{"query":"mutation($r:ThingInput!){createThing(request:$r){token}}",
       "variables":{"r":{"token":"a","bogus":1}}}'
```

## wrong variable type

```bash
curl -s -X POST http://localhost/api/things/graphql \
  -d '{"query":"mutation($r:ThingInput!){createThing(request:$r){token}}",
       "variables":{"r":"not an object"}}'
```

## undeclared variable

```bash
curl -s -X POST http://localhost/api/things/graphql \
  -d '{"query":"{thingsByToken(tokens:$t){token}}","variables":{"t":["a"]}}'
```

## unknown endpoint

```bash
curl -s -X POST http://localhost/api/nowhere/graphql \
  -d '{"query":"{thingsByToken(tokens:[\"a\"]){token}}"}'
```

## unknown field in a literal query

```bash
curl -s -X POST http://localhost/api/things/graphql \
  -d '{"query":"{thingsByToken(tokens:[\"a\"]){token zzz}}"}'
```

## not a graphql request is ignored

```bash
curl -s http://localhost/api/things/health -d '{"hello":"world"}'
```
